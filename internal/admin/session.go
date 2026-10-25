package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"unour/internal/credential"
)

const SessionTTL = time.Hour

type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

var (
	ErrUnauthorized = &Error{"admin_unauthorized", "a valid admin session is required"}
	ErrCredentials  = &Error{"admin_credentials", "incorrect admin password"}
	ErrCSRF         = &Error{"admin_csrf", "admin CSRF token is required"}
)

type Session struct {
	ExpiresAt time.Time `json:"expires_at"`
	CSRF      string    `json:"csrf"`
	Token     string    `json:"-"`
}
type Service struct {
	hash     string
	now      func() time.Time
	mu       sync.Mutex
	sessions map[[32]byte]time.Time
}

func New(cfg Config, now func() time.Time) (*Service, error) {
	if !credential.ValidHash(cfg.PasswordHash) {
		return nil, fmt.Errorf("invalid admin password hash")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{hash: cfg.PasswordHash, now: now, sessions: make(map[[32]byte]time.Time)}, nil
}
func (s *Service) Login(password string) (Session, error) {
	if len(password) > 512 || !credential.Verify(s.hash, password) {
		return Session{}, ErrCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	expires := now.Add(SessionTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for digest, exp := range s.sessions {
		if !now.Before(exp) {
			delete(s.sessions, digest)
		}
	}
	if len(s.sessions) >= 32 {
		var oldest [32]byte
		at := expires
		for digest, exp := range s.sessions {
			if exp.Before(at) {
				oldest, at = digest, exp
			}
		}
		delete(s.sessions, oldest)
	}
	s.sessions[sha256.Sum256([]byte(token))] = expires
	return Session{Token: token, CSRF: csrf(token), ExpiresAt: expires}, nil
}
func csrf(token string) string {
	digest := sha256.Sum256([]byte("unour:admin:csrf:" + token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
func (s *Service) Authenticate(token, csrfToken string, write bool) (Session, error) {
	if len(token) != 43 {
		return Session{}, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	expires, ok := s.sessions[digest]
	if !s.now().Before(expires) {
		delete(s.sessions, digest)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return Session{}, ErrUnauthorized
	}
	expected := csrf(token)
	if write && subtle.ConstantTimeCompare([]byte(expected), []byte(csrfToken)) != 1 {
		return Session{}, ErrCSRF
	}
	return Session{ExpiresAt: expires, CSRF: expected}, nil
}
func (s *Service) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sha256.Sum256([]byte(token)))
}
