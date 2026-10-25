package room

import (
	"context"
	"crypto/sha256"
	"strconv"
	"time"

	"unour/game"
	"unour/internal/bot"
)

type SoloRequest struct {
	CatalogRevision string          `json:"catalog_revision,omitempty"`
	BotCount        int             `json:"bot_count"`
	Difficulty      bot.Difficulty  `json:"difficulty"`
	Rules           []game.RuleSpec `json:"rules,omitempty"`
}

// BotUNO persists the intended reaction. A missed declaration becomes a real
// penalty, including when a restart carries the task past its deadline.
type BotUNO struct {
	WindowID uint64    `json:"window_id"`
	At       time.Time `json:"at"`
	Declare  bool      `json:"declare"`
}

// BotTask is detached and contains only this computer's private view. An empty
// Type requests a decision; UNO actions have their own independent schedule.
type BotTask struct {
	PlayerID    game.PlayerID
	Version     uint64
	DueAt       time.Time
	Type        game.Action
	WindowID    uint64
	Profile     bot.Profile
	Observation bot.Observation
}

func (s *Service) CreateSolo(ctx context.Context, accountID, name string, req SoloRequest) (JoinResult, error) {
	if !validID(accountID) {
		return JoinResult{}, ErrUnauthorized
	}
	maxPlayers := s.options.MaxPlayers
	if maxPlayers == 0 {
		maxPlayers = 10
	}
	if req.BotCount < 1 || req.BotCount >= maxPlayers || !req.Difficulty.Valid() {
		return JoinResult{}, &game.Error{Code: game.InvalidInput, Message: "choose 1–9 computers within room capacity and a valid difficulty"}
	}
	g, specs, err := s.buildGame(req.Rules, req.CatalogRevision)
	if err != nil {
		return JoinResult{}, err
	}
	id, err := randomString(12)
	if err != nil {
		return JoinResult{}, err
	}
	player, err := randomString(16)
	if err != nil {
		return JoinResult{}, err
	}
	token, err := randomString(32)
	if err != nil {
		return JoinResult{}, err
	}
	host := game.PlayerID(player)
	if err := g.Join(host, name); err != nil {
		return JoinResult{}, err
	}
	r := &Record{Game: g, HostID: host, Sessions: map[[32]byte]game.PlayerID{sha256.Sum256([]byte(token)): host}, Accounts: map[string]game.PlayerID{accountID: host}, Rules: specs, CreatedAt: s.now().UTC(), Bots: make(map[game.PlayerID]bot.Profile), BotUNO: make(map[game.PlayerID]BotUNO)}
	styles := [...]bot.Style{bot.Balanced, bot.Aggressive, bot.Cautious}
	for i := 0; i < req.BotCount; i++ {
		botID, err := randomString(16)
		if err != nil {
			return JoinResult{}, err
		}
		if err := g.Join(game.PlayerID(botID), botName(i+1)); err != nil {
			return JoinResult{}, err
		}
		r.Bots[game.PlayerID(botID)] = bot.Profile{Difficulty: req.Difficulty, Style: styles[i%len(styles)]}
	}
	before := g.View("")
	cmd := game.Command{Type: game.Start, PlayerID: host, At: s.now().UTC()}
	result, err := g.Apply(cmd)
	if err != nil {
		return JoinResult{}, err
	}
	s.observeBots(r, before, cmd, result.Events)
	if err := s.store.Create(ctx, id, r); err != nil {
		return JoinResult{}, err
	}
	return JoinResult{PlayerID: host, Token: token, Room: roomView(id, r, host)}, nil
}

func botName(number int) string { return "Bot" + strconv.Itoa(number) }

func (s *Service) observeBots(r *Record, before game.View, cmd game.Command, events []game.Event) {
	if len(r.Bots) == 0 {
		return
	}
	after := r.Game.View("")
	r.BotMemory.Observe(before, after, cmd, events)
	if after.Phase != game.Playing {
		r.BotTurnAt = time.Time{}
		r.BotUNO = nil
		return
	}
	if _, computer := r.Bots[after.Turn]; !computer {
		r.BotTurnAt = time.Time{}
	} else if r.BotTurnAt.IsZero() || before.Turn != after.Turn ||
		(cmd.PlayerID == after.Turn && cmd.Type != game.SayUNO && cmd.Type != game.PenalizeUNO) {
		delay := 900*time.Millisecond + time.Duration(s.random()*900*float64(time.Millisecond))
		if r.Game.View(after.Turn).CanPass {
			delay = 350*time.Millisecond + time.Duration(s.random()*350*float64(time.Millisecond))
		}
		r.BotTurnAt = cmd.At.Add(delay)
	}
	if r.BotUNO == nil {
		r.BotUNO = make(map[game.PlayerID]BotUNO)
	}
	for _, p := range after.Players {
		profile, computer := r.Bots[p.ID]
		if !computer {
			continue
		}
		if p.UNO == nil {
			delete(r.BotUNO, p.ID)
			continue
		}
		if r.BotUNO[p.ID].WindowID == p.UNO.ID {
			continue
		}
		forget := map[bot.Difficulty]float64{bot.Easy: 0.12, bot.Normal: 0.04, bot.Hard: 0.01, bot.Expert: 0}[profile.Difficulty]
		declare := s.random() >= forget
		at := p.UNO.Deadline.Add(100 * time.Millisecond)
		if declare {
			at = cmd.At.Add(450*time.Millisecond + time.Duration(s.random()*650*float64(time.Millisecond)))
		}
		r.BotUNO[p.ID] = BotUNO{WindowID: p.UNO.ID, At: at, Declare: declare}
	}
}

// NextBotAt is a derived persistence projection, updated with the snapshot.
func NextBotAt(r *Record) time.Time {
	if len(r.Bots) == 0 || r.Game.View("").Phase != game.Playing {
		return time.Time{}
	}
	at := r.BotTurnAt
	for _, uno := range r.BotUNO {
		if at.IsZero() || uno.At.Before(at) {
			at = uno.At
		}
	}
	return at
}

func nextBotTask(r *Record, now time.Time) *BotTask {
	v := r.Game.View("")
	if v.Phase != game.Playing {
		return nil
	}
	var task *BotTask
	if profile, computer := r.Bots[v.Turn]; computer && !r.BotTurnAt.IsZero() && !r.BotTurnAt.After(now) {
		task = &BotTask{PlayerID: v.Turn, Version: v.Version, DueAt: r.BotTurnAt, Profile: profile}
	}
	for _, p := range v.Players {
		uno, ok := r.BotUNO[p.ID]
		if !ok || p.UNO == nil || p.UNO.ID != uno.WindowID || uno.At.After(now) {
			continue
		}
		if task != nil && (task.DueAt.Before(uno.At) || task.DueAt.Equal(uno.At) && task.PlayerID < p.ID) {
			continue
		}
		action := game.PenalizeUNO
		if uno.Declare && now.Before(p.UNO.Deadline) {
			action = game.SayUNO
		}
		task = &BotTask{PlayerID: p.ID, Version: v.Version, DueAt: uno.At, Profile: r.Bots[p.ID], Type: action, WindowID: uno.WindowID}
	}
	if task != nil && task.Type == "" {
		task.Observation = bot.Observation{View: r.Game.View(task.PlayerID), Memory: r.BotMemory.Clone(), Rules: copySpecs(r.Rules), RuleModules: r.Game.RuleModules()}
	}
	return task
}

func (s *Service) NextBotTask(ctx context.Context, roomID string) (*BotTask, error) {
	var task *BotTask
	err := s.store.Read(ctx, roomID, func(r *Record) error {
		task = nextBotTask(r, s.now().UTC())
		return nil
	})
	return task, err
}

// ApplyBot is the trusted computer command boundary. Search happens outside
// storage locks, and a changed version rejects the entire old result.
func (s *Service) ApplyBot(ctx context.Context, roomID string, task BotTask, cmd game.Command) error {
	return s.store.Transact(ctx, roomID, func(r *Record) error {
		if r.Game.View("").Version != task.Version {
			return ErrConflict
		}
		current := nextBotTask(r, s.now().UTC())
		if current == nil || current.PlayerID != task.PlayerID || current.Type != task.Type || current.WindowID != task.WindowID || !current.DueAt.Equal(task.DueAt) {
			return ErrConflict
		}
		if _, ok := r.Bots[cmd.PlayerID]; !ok || cmd.PlayerID != task.PlayerID {
			return ErrForbidden
		}
		if task.Type != "" {
			if cmd.Type != current.Type || cmd.WindowID != current.WindowID {
				return ErrForbidden
			}
		} else if cmd.Type != game.Play && cmd.Type != game.Draw && cmd.Type != game.Pass && cmd.Type != game.Challenge {
			return ErrForbidden
		}
		cmd.At = s.now().UTC()
		cmd.ExpectedVersion = &task.Version
		before := r.Game.View("")
		outcome, err := r.Game.Apply(cmd)
		if err != nil {
			return err
		}
		s.observeBots(r, before, cmd, outcome.Events)
		return nil
	})
}

// RetryBot postpones a failed job in the same durable queue. This lets other
// tasks proceed, including when an UNO penalty temporarily has no card to draw.
func (s *Service) RetryBot(ctx context.Context, roomID string, task BotTask, delay time.Duration) error {
	if delay < 100*time.Millisecond || delay > 5*time.Second {
		return &game.Error{Code: game.InvalidInput, Message: "computer retry delay must be 100ms–5s"}
	}
	return s.store.Transact(ctx, roomID, func(r *Record) error {
		if r.Game.View("").Version != task.Version {
			return ErrConflict
		}
		current := nextBotTask(r, s.now().UTC())
		if current == nil || current.PlayerID != task.PlayerID || current.WindowID != task.WindowID || !current.DueAt.Equal(task.DueAt) {
			return ErrConflict
		}
		at := s.now().UTC().Add(delay)
		if task.Type == "" {
			r.BotTurnAt = at
		} else {
			uno := r.BotUNO[task.PlayerID]
			uno.At = at
			for _, p := range r.Game.View("").Players {
				if p.ID == task.PlayerID && p.UNO != nil && !at.Before(p.UNO.Deadline) {
					uno.Declare = false
				}
			}
			r.BotUNO[task.PlayerID] = uno
		}
		return nil
	})
}
