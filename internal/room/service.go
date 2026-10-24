package room

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"

	"unour/game"
)

type RuleBuilder interface {
	Build([]game.RuleSpec) (game.RuleSet, error)
}

type Config struct {
	Registry    RuleBuilder
	Now         func() time.Time
	TTL         time.Duration
	GameOptions game.Options
}

type Service struct {
	store    Store
	registry RuleBuilder
	now      func() time.Time
	ttl      time.Duration
	options  game.Options
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
	if _, err := game.New(cfg.GameOptions); err != nil {
		return nil, err
	}
	return &Service{store: store, registry: cfg.Registry, now: cfg.Now, ttl: cfg.TTL, options: cfg.GameOptions}, nil
}

type CreateRequest struct {
	ID    string          `json:"id,omitempty"`
	Rules []game.RuleSpec `json:"rules,omitempty"`
}

type View struct {
	ID        string          `json:"id"`
	HostID    game.PlayerID   `json:"host_id,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Rules     []game.RuleSpec `json:"rules"`
	State     game.View       `json:"state"`
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
	specs := req.Rules
	if specs == nil {
		specs = game.DefaultRuleSpecs()
	}
	specs = copySpecs(specs)
	rules, err := s.registry.Build(specs)
	if err != nil {
		return View{}, err
	}
	options := s.options
	options.Rules = rules
	g, err := game.New(options)
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

func (s *Service) Join(ctx context.Context, roomID, name string) (JoinResult, error) {
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
		if err := r.Game.Join(playerID, name); err != nil {
			return err
		}
		r.Sessions[digest] = playerID
		if r.HostID == "" {
			r.HostID = playerID
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
		id, err := authenticate(r, token)
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
		id, err := authenticate(r, token)
		if err != nil {
			return err
		}
		if req.Type == game.Start && id != r.HostID {
			return ErrForbidden
		}
		outcome, err := r.Game.Apply(game.Command{Type: req.Type, PlayerID: id, CardID: req.CardID, Color: req.Color, WindowID: req.WindowID, At: s.now().UTC(), ExpectedVersion: req.ExpectedVersion})
		if err != nil {
			return err
		}
		result = ActionResult{Room: roomView(roomID, r, id), Events: outcome.Events}
		return nil
	})
	return result, err
}

func (s *Service) Delete(ctx context.Context, roomID, token string) error {
	if !validID(roomID) {
		return ErrInvalid
	}
	return s.store.Delete(ctx, roomID, func(r *Record) error {
		id, err := authenticate(r, token)
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

func authenticate(r *Record, token string) (game.PlayerID, error) {
	if len(token) != 43 {
		return "", ErrUnauthorized
	}
	id, ok := r.Sessions[sha256.Sum256([]byte(token))]
	if !ok {
		return "", ErrUnauthorized
	}
	return id, nil
}

func roomView(id string, r *Record, viewer game.PlayerID) View {
	return View{ID: id, HostID: r.HostID, CreatedAt: r.CreatedAt, Rules: copySpecs(r.Rules), State: r.Game.View(viewer)}
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
