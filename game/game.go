package game

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type PlayerID string
type Phase string

const (
	Lobby    Phase = "lobby"
	Playing  Phase = "playing"
	Finished Phase = "finished"
)

type Action string

const (
	Start       Action = "start"
	Play        Action = "play"
	Draw        Action = "draw"
	Pass        Action = "pass"
	Challenge   Action = "challenge"
	SayUNO      Action = "uno"
	PenalizeUNO Action = "uno_penalty"
)

// At is supplied by the trusted application clock, never by an HTTP client.
type Command struct {
	Type            Action
	PlayerID        PlayerID
	CardID          int
	Color           Color
	WindowID        uint64
	At              time.Time
	ExpectedVersion *uint64
}

type Event struct {
	Type     string   `json:"type"`
	PlayerID PlayerID `json:"player_id,omitempty"`
	Count    int      `json:"count,omitempty"`
}

type Result struct {
	Version uint64  `json:"version"`
	Events  []Event `json:"events"`
}

type UNOWindow struct {
	ID       uint64    `json:"id"`
	Deadline time.Time `json:"deadline"`
}

type player struct {
	id            PlayerID
	name          string
	hand          []Card
	eliminated    bool
	uno           *UNOWindow
	lastUNO       uint64
	lastUNOAction Action
}

type challengeWindow struct {
	offender PlayerID
	hadColor bool
}

type state struct {
	phase       Phase
	players     []player
	draw        []Card
	discard     []Card
	turn        int
	direction   int
	active      Color
	pending     int
	pendingKind Kind
	challenge   *challengeWindow
	drawnCard   int
	winner      PlayerID
	version     uint64
}

func (s state) clone() state {
	n := s
	n.players = append([]player(nil), s.players...)
	for i := range n.players {
		n.players[i].hand = append([]Card(nil), s.players[i].hand...)
		if s.players[i].uno != nil {
			u := *s.players[i].uno
			n.players[i].uno = &u
		}
	}
	n.draw = append([]Card(nil), s.draw...)
	n.discard = append([]Card(nil), s.discard...)
	if s.challenge != nil {
		w := *s.challenge
		n.challenge = &w
	}
	return n
}

func (s *state) index(id PlayerID) int {
	for i := range s.players {
		if s.players[i].id == id {
			return i
		}
	}
	return -1
}

// next is bounded even when every player has been eliminated.
func (s *state) next(from, steps int) int {
	idx := from
	for step := 0; step < steps; step++ {
		found := false
		for tries := 0; tries < len(s.players); tries++ {
			idx = (idx + s.direction + len(s.players)) % len(s.players)
			if !s.players[idx].eliminated {
				found = true
				break
			}
		}
		if !found {
			return from
		}
	}
	return idx
}

func (s *state) advance(steps int) { s.turn = s.next(s.turn, steps); s.drawnCard = 0 }
func (s *state) cancelUNO(idx int) {
	p := &s.players[idx]
	if p.uno != nil {
		p.lastUNO = p.uno.ID
		p.lastUNOAction = "cancelled"
		p.uno = nil
	}
}

type Options struct {
	Rules       RuleSet
	Shuffle     ShuffleFunc
	MaxPlayers  int
	InitialHand int
}

// Game is deliberately not concurrency-safe. Room ownership belongs to the
// application/store boundary, so the engine is also usable without a server.
type Game struct {
	s           state
	rules       RuleSet
	shuffle     ShuffleFunc
	maxPlayers  int
	initialHand int
}

type preparedCommand struct {
	idx          int
	cardIndex    int
	card         Card
	effects      Effects
	duplicateUNO bool
}

func New(opts Options) (*Game, error) {
	if opts.MaxPlayers == 0 {
		opts.MaxPlayers = 10
	}
	if opts.InitialHand == 0 {
		opts.InitialHand = 7
	}
	if opts.MaxPlayers < 2 || opts.MaxPlayers > 10 || opts.InitialHand < 1 || opts.InitialHand > 10 || opts.MaxPlayers*opts.InitialHand >= 108 {
		return nil, fail(InvalidInput, "invalid player or initial hand limit")
	}
	if len(opts.Rules.rules) == 0 {
		var err error
		opts.Rules, err = NewRegistry().Build(DefaultRuleSpecs())
		if err != nil {
			return nil, err
		}
	}
	if opts.Shuffle == nil {
		opts.Shuffle = secureShuffle
	}
	return &Game{s: state{phase: Lobby, direction: 1}, rules: opts.Rules, shuffle: opts.Shuffle, maxPlayers: opts.MaxPlayers, initialHand: opts.InitialHand}, nil
}

func (g *Game) Join(id PlayerID, name string) error {
	name = strings.TrimSpace(name)
	if id == "" || len(id) > 64 || name == "" || utf8.RuneCountInString(name) > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fail(InvalidInput, "player ID and a name of 1–64 characters are required")
	}
	if g.s.phase != Lobby {
		return fail(InvalidState, "game has already started")
	}
	if g.s.index(id) >= 0 {
		return fail(InvalidInput, "player already joined")
	}
	if len(g.s.players) >= g.maxPlayers {
		return fail(InvalidState, "room is full")
	}
	g.s.players = append(g.s.players, player{id: id, name: name})
	g.s.version++
	return nil
}

// Apply is failure-atomic, including a rule rejection or a failed reshuffle.
// A candidate state uses copy-on-write card storage and is committed only
// after the complete operation succeeds. Events never contain hidden cards.
func (g *Game) Apply(cmd Command) (Result, error) {
	return g.apply(cmd, false)
}

// ApplyOwned is for a private simulation state with a single owner. It uses the
// same validation and transition code as Apply. Only an infallible play without
// follow-up draws reuses card storage in place; every operation that can fail
// after mutation retains Apply's copy-on-write rollback behavior.
func (g *Game) ApplyOwned(cmd Command) (Result, error) {
	return g.apply(cmd, true)
}

func (g *Game) apply(cmd Command, owned bool) (Result, error) {
	prepared, err := g.prepare(cmd)
	if err != nil {
		return Result{}, err
	}
	if prepared.duplicateUNO {
		return Result{Version: g.s.version, Events: []Event{}}, nil
	}
	idx := prepared.idx
	var n state
	if owned && cmd.Type == Play && prepared.effects.DrawNext == 0 {
		n = g.s.forkOwnedPlay()
	} else {
		n = g.s.fork()
	}
	effects := prepared.effects
	events := make([]Event, 0, 4)
	var errApply error
	switch cmd.Type {
	case Start:
		errApply = g.start(&n, effects, &events)
	case Play:
		errApply = g.play(&n, idx, prepared.cardIndex, cmd, prepared.card, effects, &events)
	case Draw:
		errApply = g.drawTurn(&n, idx, effects, &events)
	case Pass:
		n.advance(1)
		events = append(events, Event{Type: "passed", PlayerID: cmd.PlayerID})
	case Challenge:
		errApply = g.challenge(&n, idx, effects, &events)
	case SayUNO, PenalizeUNO:
		errApply = g.uno(&n, idx, cmd, &events)
	}
	if errApply != nil {
		return Result{}, errApply
	}
	n.enforceLimit(effects.HandLimit, &events)
	n.settle(&events)
	n.version++
	g.s = n
	return Result{Version: n.version, Events: events}, nil
}

// CanApply runs the same state, rule, and action validation as Apply without
// mutating the game or invoking the shuffle callback. Runtime failures such as
// entropy errors remain the responsibility of Apply.
func (g *Game) CanApply(cmd Command) error {
	_, err := g.prepare(cmd)
	return err
}

func (g *Game) prepare(cmd Command) (preparedCommand, error) {
	prepared := preparedCommand{cardIndex: -1}
	if cmd.ExpectedVersion != nil && *cmd.ExpectedVersion != g.s.version {
		return prepared, fail(VersionConflict, "state version is stale")
	}
	idx := g.s.index(cmd.PlayerID)
	if idx < 0 {
		return prepared, fail(InvalidInput, "player is not in the game")
	}
	if cmd.Type == Start {
		if g.s.phase != Lobby || idx != 0 || len(g.s.players) < 2 {
			return prepared, fail(InvalidState, "only the host can start a lobby with at least two players")
		}
	} else {
		if g.s.phase != Playing || g.s.players[idx].eliminated {
			return prepared, fail(InvalidState, "player cannot act in this game state")
		}
		if cmd.Type != SayUNO && cmd.Type != PenalizeUNO && idx != g.s.turn {
			return prepared, fail(NotYourTurn, "it is another player's turn")
		}
	}
	if (cmd.Type == SayUNO || cmd.Type == PenalizeUNO) && cmd.WindowID != 0 && g.s.players[idx].lastUNO == cmd.WindowID && g.s.players[idx].lastUNOAction == cmd.Type {
		prepared.duplicateUNO = true
		return prepared, nil
	}
	ctx := Context{s: &g.s, command: cmd}
	if cmd.Type == Play {
		for i, c := range g.s.players[idx].hand {
			if c.ID == cmd.CardID {
				prepared.cardIndex = i
				ctx.card = c
				break
			}
		}
		if prepared.cardIndex < 0 {
			return prepared, fail(CardNotOwned, "card is not in your hand")
		}
	}
	effects := Effects{TurnSteps: 1}
	for _, rule := range g.rules.rules {
		if err := rule.Evaluate(ctx, &effects); err != nil {
			return prepared, &Error{Code: RuleViolation, Message: "rule " + rule.ID() + " rejected the action", Cause: err}
		}
	}
	if err := effects.validate(); err != nil {
		return prepared, err
	}
	if err := g.validateAction(&g.s, idx, cmd, ctx.card, effects); err != nil {
		return prepared, err
	}
	prepared.idx = idx
	prepared.card = ctx.card
	prepared.effects = effects
	return prepared, nil
}

func (s *state) enforceLimit(limit int, events *[]Event) {
	if limit == 0 || s.phase != Playing {
		return
	}
	for i := range s.players {
		p := &s.players[i]
		if !p.eliminated && len(p.hand) >= limit {
			p.eliminated = true
			s.cancelUNO(i)
			*events = append(*events, Event{Type: "eliminated", PlayerID: p.id})
		}
	}
	if s.players[s.turn].eliminated {
		s.advance(1)
	}
}

func (s *state) settle(events *[]Event) {
	if s.phase != Playing {
		return
	}
	alive, remaining := 0, PlayerID("")
	for _, p := range s.players {
		if p.eliminated {
			continue
		}
		alive++
		remaining = p.id
		if len(p.hand) == 0 && s.pending == 0 {
			s.finish(p.id, events)
			return
		}
	}
	if alive <= 1 {
		s.finish(remaining, events)
	}
}

func (s *state) finish(winner PlayerID, events *[]Event) {
	s.phase = Finished
	s.winner = winner
	s.drawnCard = 0
	s.challenge = nil
	for i := range s.players {
		s.cancelUNO(i)
	}
	*events = append(*events, Event{Type: "finished", PlayerID: winner})
}
