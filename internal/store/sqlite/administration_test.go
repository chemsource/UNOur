package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"unour/game"
	"unour/internal/account"
	"unour/internal/bot"
	"unour/internal/room"
)

func TestAdministrativeEditIsAtomicAuditedAndDurable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "admin.db")
	store := openTest(t, path, nil, 10)
	service := serviceTest(t, store, nil)
	a, b := started(t, service)
	manager := room.NewManager(service, store)
	listed, err := manager.List(ctx, 10, 0)
	if err != nil || listed.Total != 1 || len(listed.Rooms) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	before, err := manager.Inspect(ctx, "table")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.State.Players[0].Hand) != 2 || len(before.State.Players[1].Hand) != 2 || len(before.Deck) != 103 {
		t.Fatal("admin cannot inspect all cards")
	}
	req := room.EditRequest{CardEdit: game.CardEdit{Operation: "swap", CardID: before.State.Players[0].Hand[0].ID, ReplacementID: before.State.Players[1].Hand[0].ID}, ExpectedVersion: before.State.Version, ExpectedRevision: before.Revision}
	after, err := manager.Edit(ctx, "table", req)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Players[0].Hand[0].ID != 26 || after.State.Version != before.State.Version+1 {
		t.Fatal("edit not applied")
	}
	if _, err := manager.Edit(ctx, "table", req); !errors.Is(err, room.ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	entries, err := store.AuditLog(ctx, 10, 0)
	if err != nil || len(entries.Entries) != 1 {
		t.Fatal("failed command was audited or success was not audited")
	}
	// Audit and room commit must roll back together if audit storage is unavailable.
	if _, err := store.write.Exec(`CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	req.ExpectedVersion, req.ExpectedRevision = after.State.Version, after.Revision
	if _, err := manager.Edit(ctx, "table", req); err == nil {
		t.Fatal("edit succeeded without audit")
	}
	unchanged, _ := manager.Inspect(ctx, "table")
	if !reflect.DeepEqual(after, unchanged) {
		t.Fatal("audit failure committed game mutation")
	}
	store.write.Exec(`DROP TRIGGER reject_admin_audit`)
	public, err := service.State(ctx, "table", a.Token)
	if err != nil || len(public.State.Players[1].Hand) != 0 {
		t.Fatal("admin visibility leaked to player")
	}
	other, err := service.State(ctx, "table", b.Token)
	if err != nil || len(other.State.Players[0].Hand) != 0 {
		t.Fatal("admin visibility leaked to opponent")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTest(t, path, nil, 10)
	manager = room.NewManager(serviceTest(t, store, nil), store)
	restored, err := manager.Inspect(ctx, "table")
	if err != nil || !reflect.DeepEqual(after, restored) {
		t.Fatalf("restart changed admin edit: %v", err)
	}
}

func TestAdminEditInvalidatesBotDecisionAndPrivateInference(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := openTest(t, filepath.Join(t.TempDir(), "bot-admin.db"), clock, 10)
	service, err := room.New(store, room.Config{Now: clock, BotRandom: func() float64 { return .5 }})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.CreateSolo(ctx, "human-account", "Human", room.SoloRequest{BotCount: 1, Difficulty: bot.Easy})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []game.Action{game.Draw, game.Pass} {
		if _, err := service.Execute(ctx, joined.Room.ID, joined.Token, room.ActionRequest{Type: action}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(5 * time.Second)
	task, err := service.NextBotTask(ctx, joined.Room.ID)
	if err != nil || task == nil {
		t.Fatal("missing due bot decision", err)
	}
	manager := room.NewManager(service, store)
	before, err := manager.Inspect(ctx, joined.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Edit(ctx, joined.Room.ID, room.EditRequest{CardEdit: game.CardEdit{Operation: "swap", CardID: before.State.Players[0].Hand[0].ID, ReplacementID: before.Deck[0].ID}, ExpectedVersion: before.State.Version, ExpectedRevision: before.Revision}); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyBot(ctx, joined.Room.ID, *task, game.Command{Type: game.Draw, PlayerID: task.PlayerID}); !errors.Is(err, room.ErrConflict) {
		t.Fatal("stale computer decision was accepted", err)
	}
	updated, err := service.NextBotTask(ctx, joined.Room.ID)
	if err != nil || updated == nil || updated.Version != before.State.Version+1 {
		t.Fatal("computer was not rescheduled", err)
	}
	if len(updated.Observation.Memory.Beliefs) != 0 {
		t.Fatal("computer retained stale inference")
	}
	for _, p := range updated.Observation.View.Players {
		if p.ID != task.PlayerID && len(p.Hand) != 0 {
			t.Fatal("administrator cards leaked into bot search")
		}
	}
	if err := service.ApplyBot(ctx, joined.Room.ID, *updated, game.Command{Type: game.Draw, PlayerID: task.PlayerID}); err != nil {
		t.Fatal("new computer decision failed", err)
	}
}

func TestAccountManagementRollsBackWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	store := openTest(t, filepath.Join(t.TempDir(), "audit-user.db"), nil, 10)
	accounts, _ := account.New(store, account.Config{})
	login, err := accounts.Register(ctx, "AuditUser", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.Exec(`CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := account.NewManager(store).SetDisabled(ctx, login.User.ID, true); err == nil {
		t.Fatal("management succeeded without audit")
	}
	if _, err := accounts.Authenticate(ctx, login.Token); err != nil {
		t.Fatal("audit failure revoked account session", err)
	}
	record, err := store.AccountByName(ctx, "audituser")
	if err != nil || record.Disabled || record.CredentialVersion != 0 {
		t.Fatal("audit failure changed account", err)
	}
}
