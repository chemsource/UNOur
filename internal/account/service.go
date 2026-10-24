package account

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const SessionTTL = 30 * 24 * time.Hour

type Config struct{ Now func() time.Time }
type Service struct {
	store       Store
	now         func() time.Time
	avatarSlots chan struct{}
}
type Session struct {
	User      Profile   `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
	Token     string    `json:"-"`
}

func New(store Store, cfg Config) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("account store is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{store: store, now: cfg.Now, avatarSlots: make(chan struct{}, 1)}, nil
}
func username(value string) (string, string, error) {
	name := strings.TrimSpace(value)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 3 || utf8.RuneCountInString(name) > 24 {
		return "", "", &Error{"invalid_username", "username must contain 3–24 Chinese characters, ASCII letters, digits, underscores or hyphens"}
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || unicode.Is(unicode.Han, r)) {
			return "", "", &Error{"invalid_username", "username contains unsupported characters"}
		}
	}
	return name, strings.ToLower(name), nil
}
func validPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 8 && utf8.RuneCountInString(password) <= 14
}
func (s *Service) Register(ctx context.Context, name, password string) (Session, error) {
	name, key, err := username(name)
	if err != nil {
		return Session{}, err
	}
	if !validPassword(password) {
		return Session{}, &Error{"invalid_password", "password must contain 8–14 characters"}
	}
	encoded, err := hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	id, err := randomToken(16)
	if err != nil {
		return Session{}, err
	}
	user := Profile{ID: id, Username: name, CreatedAt: s.now().UTC()}
	result, firstSession, err := s.issueSession(user)
	if err != nil {
		return Session{}, err
	}
	if err := s.store.CreateAccount(ctx, Record{Profile: user, UsernameKey: key, PasswordHash: encoded}, firstSession); err != nil {
		return Session{}, err
	}
	return result, nil
}
func (s *Service) Login(ctx context.Context, name, password string) (Session, error) {
	_, key, nameErr := username(name)
	if !validPassword(password) || nameErr != nil {
		return Session{}, ErrCredentials
	}
	record, err := s.store.AccountByName(ctx, key)
	if errors.Is(err, ErrNotFound) {
		// Keep unknown-user attempts comparable to password verification without
		// creating an account or leaking whether the username exists.
		_, _ = pbkdf2.Key(sha256.New, password, make([]byte, 32), passwordIterations, 32)
		return Session{}, ErrCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if !verifyPassword(record.PasswordHash, password) {
		return Session{}, ErrCredentials
	}
	return s.session(ctx, record.Profile)
}
func (s *Service) session(ctx context.Context, user Profile) (Session, error) {
	result, record, err := s.issueSession(user)
	if err != nil {
		return Session{}, err
	}
	if err := s.store.CreateAccountSession(ctx, record); err != nil {
		return Session{}, err
	}
	return result, nil
}
func (s *Service) issueSession(user Profile) (Session, SessionRecord, error) {
	token, err := randomToken(32)
	if err != nil {
		return Session{}, SessionRecord{}, err
	}
	now := s.now().UTC()
	expires := now.Add(SessionTTL)
	return Session{User: user, ExpiresAt: expires, Token: token}, SessionRecord{Digest: sha256.Sum256([]byte(token)), AccountID: user.ID, CreatedAt: now, ExpiresAt: expires}, nil
}
func (s *Service) Authenticate(ctx context.Context, token string) (Profile, error) {
	if len(token) != 43 {
		return Profile{}, ErrUnauthorized
	}
	return s.store.SessionAccount(ctx, sha256.Sum256([]byte(token)), s.now())
}
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteAccountSession(ctx, sha256.Sum256([]byte(token)))
}
func (s *Service) Profiles(ctx context.Context, ids []string) (map[string]Profile, error) {
	return s.store.PublicProfiles(ctx, ids)
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
