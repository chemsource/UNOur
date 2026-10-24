package sqlite

import (
	"context"
	"errors"
	"path/filepath"
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
