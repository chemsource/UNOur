package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unour/internal/account"
)

func TestRegistrationRollsBackWhenInitialSessionCannotBeStored(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, filepath.Join(t.TempDir(), "atomic.db"), nil, 10)
	a, err := account.New(s, account.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `CREATE TRIGGER reject_session BEFORE INSERT ON account_sessions BEGIN SELECT RAISE(ABORT,'session storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Register(ctx, "AtomicUser", "12345678"); err == nil {
		t.Fatal("registration succeeded despite session failure")
	}
	if _, err := s.AccountByName(ctx, "atomicuser"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("failed registration left a claimed username: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER reject_session`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Register(ctx, "AtomicUser", "12345678"); err != nil {
		t.Fatalf("cannot retry failed registration: %v", err)
	}
}

func TestPublicProfilesLoadsMultipleBatchesWithoutPrivateFields(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, filepath.Join(t.TempDir(), "profiles.db"), nil, 10)
	service, err := account.New(s, account.Config{})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.Register(ctx, "TemplateUser", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	template, err := s.AccountByName(ctx, "templateuser")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{registered.User.ID}
	for i := 0; i < 130; i++ {
		id := fmt.Sprintf("profile-%d", i)
		record := template
		record.Profile.ID = id
		record.Profile.Username = fmt.Sprintf("Profile%d", i)
		record.UsernameKey = strings.ToLower(record.Profile.Username)
		session := account.SessionRecord{Digest: sha256.Sum256([]byte(id)), AccountID: id, CreatedAt: record.Profile.CreatedAt, ExpiresAt: registered.ExpiresAt}
		if err := s.CreateAccount(ctx, record, session); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	profiles, err := s.PublicProfiles(ctx, append(ids, "missing", ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 131 || profiles["profile-129"].Username != "Profile129" {
		t.Fatal("profile batch lost members")
	}
	if _, exists := profiles["missing"]; exists {
		t.Fatal("unknown profile fabricated")
	}
	empty, err := s.PublicProfiles(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatal("empty profile lookup failed")
	}
}
