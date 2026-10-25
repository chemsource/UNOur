package admin

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAdminSessionsAreSeparateExpiringAndRevocable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	if err := WriteConfig(path, "an independent password"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	s, err := New(cfg, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login("incorrect password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	session, err := s.Login("an independent password")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Token) != 43 || session.CSRF == "" {
		t.Fatal("missing secure credentials")
	}
	if _, err := s.Authenticate(session.Token, "incorrect", true); err == nil {
		t.Fatal("incorrect CSRF accepted")
	}
	if _, err := s.Authenticate(session.Token, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(session.Token, session.CSRF, true); err != nil {
		t.Fatal(err)
	}
	s.Logout(session.Token)
	if _, err := s.Authenticate(session.Token, "", false); err == nil {
		t.Fatal("revoked token accepted")
	}
	session, _ = s.Login("an independent password")
	now = now.Add(time.Hour)
	if _, err := s.Authenticate(session.Token, "", false); err == nil {
		t.Fatal("expired token accepted")
	}
	fresh, _ := New(cfg, nil)
	if _, err := fresh.Authenticate(session.Token, "", false); err == nil {
		t.Fatal("restart retained admin session")
	}
}
