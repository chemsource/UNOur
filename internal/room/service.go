package room

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	mathrand "math/rand/v2"
	"time"

	"unour/game"
	"unour/internal/bot"
)

type RuleBuilder interface {
	Build([]game.RuleSpec) (game.RuleSet, error)
}

type Config struct {
	Registry    RuleBuilder
	Now         func() time.Time
	TTL         time.Duration
	GameOptions game.Options
	// BotRandom supplies non-security timing randomness and must be concurrent-safe.
	BotRandom func() float64
}

type Service struct {
	store    Store
	registry RuleBuilder
	now      func() time.Time
	ttl      time.Duration
	options  game.Options
	random   func() float64
}

func New(store Store, cfg Config) (*Service, error) {
	if store == nil {
		return nil, &Error{Code: "invalid_config", Message: "room store is required"}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TTL < 0 {
		return nil, &Error{Code: "invalid_config", Message: "room TTL must not be negative"}
	}
	if cfg.Registry == nil {
		cfg.Registry = game.NewRegistry()
	}
	if cfg.BotRandom == nil {
		cfg.BotRandom = mathrand.Float64
	}
	if _, err := game.New(cfg.GameOptions); err != nil {
		return nil, err
	}
	return &Service{store: store, registry: cfg.Registry, now: cfg.Now, ttl: cfg.TTL, options: cfg.GameOptions, random: cfg.BotRandom}, nil
}

type CreateRequest struct {
	ID    string          `json:"id,omitempty"`
	Rules []game.RuleSpec `json:"rules,omitempty"`
}

type View struct {
	RosterVersion uint64          `json:"roster_version"`
	Departed      []game.PlayerID `json:"departed"`
	Role          string          `json:"role"`
	Spectators    []Spectator     `json:"spectators"`
	RematchVotes  []game.PlayerID `json:"rematch_votes"`
	Round         uint64          `json:"round"`
	Revision      uint64          `json:"revision"`
	// Accounts is detached metadata for the HTTP profile adapter, never JSON.
	Accounts  map[game.PlayerID]string      `json:"-"`
	ID        string                        `json:"id"`
	HostID    game.PlayerID                 `json:"host_id,omitempty"`
	CreatedAt time.Time                     `json:"created_at"`
	Rules     []game.RuleSpec               `json:"rules"`
	State     game.View                     `json:"state"`
	Bots      map[game.PlayerID]bot.Profile `json:"bots,omitempty"`
}

type JoinResult struct {
	PlayerID game.PlayerID `json:"player_id"`
	Token    string        `json:"token"`
	Room     View          `json:"room"`
}

type ActionRequest struct {
	Type            game.Action
	CardID          int
	Color           game.Color
	WindowID        uint64
	ExpectedVersion *uint64
}

type ActionResult struct {
	Room   View         `json:"room"`
	Events []game.Event `json:"events"`
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (View, error) {
	if req.ID == "" {
		id, err := randomString(12)
		if err != nil {
			return View{}, err
		}
		req.ID = id
	}
	if !validID(req.ID) {
		return View{}, ErrInvalid
	}
	g, specs, err := s.buildGame(req.Rules)
	if err != nil {
		return View{}, err
	}
	record := &Record{Game: g, Sessions: make(map[[32]byte]game.PlayerID), Rules: specs, CreatedAt: s.now().UTC()}
	view := roomView(req.ID, record, "")
	if err := s.store.Create(ctx, req.ID, record); err != nil {
		return View{}, err
	}
	return view, nil
}

func (s *Service) buildGame(specs []game.RuleSpec) (*game.Game, []game.RuleSpec, error) {
	if specs == nil {
		specs = game.DefaultRuleSpecs()
	}
	specs = copySpecs(specs)
	rules, err := s.registry.Build(specs)
	if err != nil {
		return nil, nil, err
	}
	options := s.options
	options.Rules = rules
	g, err := game.New(options)
	if err != nil {
		return nil, nil, err
	}
	return g, specs, nil
}

func (s *Service) Join(ctx context.Context, roomID, name string) (JoinResult, error) {
	return s.join(ctx, roomID, name, "", false)
}

// JoinAccount joins a lobby seat or reserves a next-round seat while viewing.
// An existing account recovers its identity without adding another member.
// Re-entry rotates its room token; joining a different account cannot claim it.
func (s *Service) JoinAccount(ctx context.Context, roomID, accountID, name string) (JoinResult, error) {
	if !validID(accountID) {
		return JoinResult{}, ErrUnauthorized
	}
	return s.join(ctx, roomID, name, accountID, false)
}

func (s *Service) join(ctx context.Context, roomID, name, accountID string, spectate bool) (JoinResult, error) {
	if !validID(roomID) {
		return JoinResult{}, ErrInvalid
	}
	id, err := randomString(16)
	if err != nil {
		return JoinResult{}, err
	}
	token, err := randomString(32)
	if err != nil {
		return JoinResult{}, err
	}
	playerID := game.PlayerID(id)
	digest := sha256.Sum256([]byte(token))
	var result JoinResult
	err = s.store.Transact(ctx, roomID, func(r *Record) error {

		if _, collision := r.Sessions[digest]; collision {
			return &game.Error{Code: game.InternalFailure, Message: "could not allocate a unique player session"}
		}
		existing := r.Accounts[accountID]
		if accountID != "" && existing != "" {
			playerID = existing
		} else {
			if spectate || r.Game.View("").Phase != game.Lobby {
				// Validate identity/name using the same boundary as seated players.
				probe, _, e := s.buildGame(r.Rules)
				if e != nil {
					return e
				}
				if e = probe.Join(playerID, name); e != nil {
					return e
				}
				if len(r.Spectators) >= 50 {
					return ErrCapacity
				}
				queued := !spectate
				if queued && nextSeatCount(r) >= r.Game.Checkpoint().MaxPlayers {
					return ErrCapacity
				}
				r.Spectators = append(r.Spectators, Spectator{ID: playerID, Name: probe.View("").Players[0].Name, NextRound: queued})
				if queued {
					r.RosterVersion++
					r.RematchVotes = nil
				}
			} else {
				if err := r.Game.Join(playerID, name); err != nil {
					return err
				}
				r.RosterVersion++
			}
			r.Revision++
		}
		if accountID != "" {
			if r.Accounts == nil {
				r.Accounts = make(map[string]game.PlayerID)
			}
			r.Accounts[accountID] = playerID
		}
		for old, id := range r.Sessions {
			if id == playerID {
				delete(r.Sessions, old)
			}
		}
		r.Sessions[digest] = playerID
		if r.HostID == "" {
			r.HostID = playerID
		}
		if syncLobbyHost(r) {
			r.Revision++
		}
		result = JoinResult{PlayerID: playerID, Token: token, Room: roomView(roomID, r, playerID)}
		return nil
	})
	return result, err
}

func (s *Service) State(ctx context.Context, roomID, token string) (View, error) {
	if !validID(roomID) {
		return View{}, ErrInvalid
	}
	var view View
	err := s.store.Read(ctx, roomID, func(r *Record) error {
		id, err := authenticate(ctx, r, token)
		if err != nil {
			return err
		}
		view = roomView(roomID, r, id)
		return nil
	})
	return view, err
}

func (s *Service) Execute(ctx context.Context, roomID, token string, req ActionRequest) (ActionResult, error) {
	if !validID(roomID) {
		return ActionResult{}, ErrInvalid
	}
	var result ActionResult
	err := s.store.Transact(ctx, roomID, func(r *Record) error {
		id, err := authenticate(ctx, r, token)
		if err != nil {
			return err
		}
		for _, p := range r.Spectators {
			if p.ID == id {
				return &Error{Code: "spectator_read_only", Message: "spectators cannot play"}
			}
		}
		if req.Type == game.Start && id != r.HostID {
			return ErrForbidden
		}
		command := game.Command{Type: req.Type, PlayerID: id, CardID: req.CardID, Color: req.Color, WindowID: req.WindowID, At: s.now().UTC(), ExpectedVersion: req.ExpectedVersion}
		before := r.Game.View("")
		outcome, err := r.Game.Apply(command)
		if err != nil {
			return err
		}
		result = ActionResult{Room: roomView(roomID, r, id), Events: outcome.Events}
		s.observeBots(r, before, command, outcome.Events)
		return nil
	})
	return result, err
}

func (s *Service) Delete(ctx context.Context, roomID, token string) error {
	if !validID(roomID) {
		return ErrInvalid
	}
	return s.store.Delete(ctx, roomID, func(r *Record) error {
		id, err := authenticate(ctx, r, token)
		if err != nil {
			return err
		}
		if id != r.HostID {
			return ErrForbidden
		}
		return nil
	})
}

func (s *Service) Sweep(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.ttl == 0 {
		return 0, nil
	}
	return s.store.Sweep(ctx, s.now().Add(-s.ttl))
}

func authenticate(ctx context.Context, r *Record, token string) (game.PlayerID, error) {
	if len(token) != 43 {
		return "", ErrUnauthorized
	}
	id, ok := r.Sessions[sha256.Sum256([]byte(token))]
	if !ok {
		return "", ErrUnauthorized
	}
	if accountID, scoped := ctx.Value(accountKey{}).(string); scoped && r.Accounts[accountID] != id {
		return "", ErrUnauthorized
	}
	return id, nil
}

func roomView(id string, r *Record, viewer game.PlayerID) View {
	accounts := make(map[game.PlayerID]string, len(r.Accounts))
	for accountID, playerID := range r.Accounts {
		accounts[playerID] = accountID
	}
	bots := make(map[game.PlayerID]bot.Profile, len(r.Bots))
	for id, profile := range r.Bots {
		bots[id] = profile
	}
	state := r.Game.View(viewer)
	// Canonical display names also apply to already persisted computer rooms.
	botNumber := 0
	for i := range state.Players {
		if _, computer := bots[state.Players[i].ID]; computer {
			botNumber++
			state.Players[i].Name = botName(botNumber)
		}
	}
	role := "player"
	for _, observer := range r.Spectators {
		if observer.ID == viewer {
			role = "spectator"
		}
	}
	observers := append([]Spectator{}, r.Spectators...)
	votes := make([]game.PlayerID, 0, len(r.RematchVotes))
	for _, p := range state.Players {
		if r.RematchVotes[p.ID] {
			votes = append(votes, p.ID)
		}
	}
	for _, p := range observers {
		if r.RematchVotes[p.ID] {
			votes = append(votes, p.ID)
		}
	}
	departed := make([]game.PlayerID, 0, len(r.Departed))
	for _, p := range state.Players {
		if r.Departed[p.ID] {
			departed = append(departed, p.ID)
		}
	}
	return View{RosterVersion: r.RosterVersion, Departed: departed, Role: role, Spectators: observers, RematchVotes: votes, Round: roundNumber(r), Revision: r.Revision + state.Version, ID: id, HostID: r.HostID, CreatedAt: r.CreatedAt, Rules: copySpecs(r.Rules), State: state, Accounts: accounts, Bots: bots}
}

type accountKey struct{}

// WithAccount scopes room credentials to an identity already authenticated by
// a trusted adapter. Internal callers without this scope use room tokens alone.
func WithAccount(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, accountKey{}, accountID)
}

func copySpecs(specs []game.RuleSpec) []game.RuleSpec {
	out := make([]game.RuleSpec, len(specs))
	for i, spec := range specs {
		out[i] = game.RuleSpec{Name: spec.Name, Options: append(json.RawMessage(nil), spec.Options...)}
	}
	return out
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", &game.Error{Code: game.InternalFailure, Message: "could not generate a secure session", Cause: err}
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func validID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
