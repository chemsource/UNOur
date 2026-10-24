package account_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"unour/internal/account"
	"unour/internal/store/sqlite"
)

func TestAccountValidationPersistenceAndSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "account.db")
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	open := func() (*sqlite.Store, *account.Service) {
		s, err := sqlite.Open(sqlite.Config{Path: path, MaxRooms: 10})
		if err != nil {
			t.Fatal(err)
		}
		a, err := account.New(s, account.Config{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return s, a
	}
	s, a := open()
	for _, tc := range []struct{ name, password string }{{"ab", "123456789"}, {"Alice", "1234567"}, {"<script>", "123456789"}} {
		if _, err := a.Register(ctx, tc.name, tc.password); err == nil {
			t.Fatalf("accepted invalid registration: %s", tc.name)
		}
	}
	registered, err := a.Register(ctx, " Alice ", "valid-password")
	if err != nil {
		t.Fatal(err)
	}
	if registered.User.Username != "Alice" || registered.User.AvatarURL != "" || registered.Token == "" {
		t.Fatal("invalid registered profile")
	}
	if _, err := a.Register(ctx, "ALICE", "other-password"); !errors.Is(err, account.ErrUsernameTaken) {
		t.Fatalf("case-insensitive duplicate: %v", err)
	}
	if _, err := a.Login(ctx, "alice", "incorrect-password"); !errors.Is(err, account.ErrCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := a.Login(ctx, "missing", "incorrect-password"); !errors.Is(err, account.ErrCredentials) {
		t.Fatalf("unknown user must return same error: %v", err)
	}
	logged, err := a.Login(ctx, "ALICE", "valid-password")
	if err != nil {
		t.Fatal(err)
	}
	if logged.User.ID != registered.User.ID || logged.Token == registered.Token {
		t.Fatal("login must issue independent session for same identity")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, a = open()
	defer s.Close()
	profile, err := a.Authenticate(ctx, registered.Token)
	if err != nil || profile.ID != registered.User.ID {
		t.Fatalf("session did not survive restart: %v", err)
	}
	if err := a.Logout(ctx, registered.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, registered.Token); !errors.Is(err, account.ErrUnauthorized) {
		t.Fatal("logout did not revoke session")
	}
	if _, err := a.Authenticate(ctx, logged.Token); err != nil {
		t.Fatal("logout revoked another session")
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, err := a.Authenticate(ctx, logged.Token); !errors.Is(err, account.ErrUnauthorized) {
		t.Fatal("expired session accepted")
	}
}

func TestConcurrentUsernameUniqueness(t *testing.T) {
	s, err := sqlite.Open(sqlite.Config{Path: filepath.Join(t.TempDir(), "accounts.db"), MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := account.New(s, account.Config{})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for _, name := range []string{"SameName", "SAMENAME", "samename"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			_, err := a.Register(context.Background(), n, "valid-password")
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	created := 0
	for err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, account.ErrUsernameTaken) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created %d duplicate accounts", created)
	}
}

func TestAvatarValidationNormalizationAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "avatar.db")
	s, err := sqlite.Open(sqlite.Config{Path: path, MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := account.New(s, account.Config{})
	login, err := a.Register(ctx, "头像用户", "valid-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte(`<svg onload="alert(1)"/>`), make([]byte, (2<<20)+1)} {
		if _, err := a.SetAvatar(ctx, login.User.ID, data); err == nil {
			t.Fatal("invalid avatar accepted")
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 400, 200))); err != nil {
		t.Fatal(err)
	}
	profile, err := a.SetAvatar(ctx, login.User.ID, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if profile.AvatarURL == "" {
		t.Fatal("avatar URL missing")
	}
	digest := profile.AvatarURL[len("/api/v1/avatars/"):]
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(sqlite.Config{Path: path, MaxRooms: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ = account.New(s, account.Config{})
	data, err := a.Avatar(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 256 || cfg.Height != 256 {
		t.Fatalf("avatar not normalized: %+v %v", cfg, err)
	}
	restored, err := a.Authenticate(ctx, login.Token)
	if err != nil || restored.AvatarURL != profile.AvatarURL {
		t.Fatal("avatar did not persist")
	}
	reset, err := a.ResetAvatar(ctx, login.User.ID)
	if err != nil || reset.AvatarURL != "" {
		t.Fatal("avatar reset failed")
	}
}
