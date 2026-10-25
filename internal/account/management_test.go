package account_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"unour/internal/account"
	"unour/internal/store/sqlite"
)

// This catches stale login issuance and accidental reactivation of revoked sessions.
func TestManagementRevokesSessionsAndPersistsBans(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "accounts.db")
	store, err := sqlite.Open(sqlite.Config{Path: path, MaxRooms: 10})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := account.New(store, account.Config{})
	manager := account.NewManager(store)
	registered, err := svc.Register(ctx, "ManagedUser", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.AccountByName(ctx, "manageduser")
	stale := account.SessionRecord{AccountID: record.Profile.ID, CredentialVersion: record.CredentialVersion}
	stale.Digest[0] = 1
	if err := manager.SetDisabled(ctx, registered.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, registered.Token); !errors.Is(err, account.ErrUnauthorized) {
		t.Fatalf("banned session: %v", err)
	}
	if _, err := svc.Login(ctx, "ManagedUser", "old-password"); !errors.Is(err, account.ErrCredentials) {
		t.Fatalf("banned login: %v", err)
	}
	if err := store.CreateAccountSession(ctx, stale); !errors.Is(err, account.ErrCredentials) {
		t.Fatalf("stale login after ban: %v", err)
	}
	users, err := manager.List(ctx, "MANAGED", 20, 0)
	if err != nil || len(users.Users) != 1 || !users.Users[0].Disabled || users.Users[0].ActiveSessions != 0 {
		t.Fatalf("list: %+v %v", users, err)
	}
	literal, err := manager.List(ctx, "_", 20, 0)
	if err != nil || literal.Total != 0 {
		t.Fatal("search treated literal underscore as SQL wildcard", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(sqlite.Config{Path: path, MaxRooms: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc, _ = account.New(store, account.Config{})
	manager = account.NewManager(store)
	users, _ = manager.List(ctx, "Managed", 20, 0)
	if !users.Users[0].Disabled {
		t.Fatal("ban did not survive restart")
	}
	if err := manager.SetDisabled(ctx, registered.User.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, registered.Token); !errors.Is(err, account.ErrUnauthorized) {
		t.Fatal("unban revived old session")
	}
	login, err := svc.Login(ctx, "ManagedUser", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	currentRecord, err := store.AccountByName(ctx, "manageduser")
	if err != nil {
		t.Fatal(err)
	}
	stale.CredentialVersion = currentRecord.CredentialVersion
	if err := manager.ResetPassword(ctx, registered.User.ID, "  新密码🙂🙂🙂  "); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccountSession(ctx, stale); !errors.Is(err, account.ErrCredentials) {
		t.Fatal("password reset accepted old login issuance", err)
	}
	if _, err := svc.Authenticate(ctx, login.Token); !errors.Is(err, account.ErrUnauthorized) {
		t.Fatal("reset kept session")
	}
	if _, err := svc.Login(ctx, "ManagedUser", "old-password"); !errors.Is(err, account.ErrCredentials) {
		t.Fatal("old password accepted")
	}
	login, err = svc.Login(ctx, "ManagedUser", "  新密码🙂🙂🙂  ")
	if err != nil {
		t.Fatal(err)
	}
	currentRecord, err = store.AccountByName(ctx, "manageduser")
	if err != nil {
		t.Fatal(err)
	}
	stale.CredentialVersion = currentRecord.CredentialVersion
	if err := manager.RevokeSessions(ctx, login.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.Token); !errors.Is(err, account.ErrUnauthorized) {
		t.Fatal("forced logout retained session")
	}
	if err := store.CreateAccountSession(ctx, stale); !errors.Is(err, account.ErrCredentials) {
		t.Fatal("forced logout accepted an in-flight login", err)
	}
	audit, err := store.AuditLog(ctx, 20, 0)
	if err != nil || len(audit.Entries) != 4 {
		t.Fatalf("audit: %+v %v", audit, err)
	}
}
