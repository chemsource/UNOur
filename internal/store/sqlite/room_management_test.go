package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unour/game"
	"unour/internal/room"
)

func roomGuard(v room.Inspection) room.DeleteTarget {
	return room.DeleteTarget{ID: v.ID, Instance: v.Instance, Phase: v.State.Phase}
}

func TestBulkDeleteIsPermanentGuardedAndIndividuallyAudited(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rooms.db")
	store := openTest(t, path, nil, 10)
	svc := serviceTest(t, store, nil)
	a, _ := started(t, svc)
	m := room.NewManager(svc, store)
	old, _ := m.Inspect(ctx, "table")
	if _, err := m.Delete(ctx, []room.DeleteTarget{roomGuard(old), roomGuard(old)}); err == nil {
		t.Fatal("duplicate batch accepted")
	}
	for _, batch := range [][]room.DeleteTarget{nil, make([]room.DeleteTarget, room.MaxDeleteBatch+1), {roomGuard(old), {ID: "invalid", Phase: game.Lobby}}} {
		if _, err := m.Delete(ctx, batch); err == nil {
			t.Fatal("invalid batch accepted")
		}
	}
	if _, err := svc.State(ctx, "table", a.Token); err != nil {
		t.Fatal("invalid batch partially deleted rooms")
	}
	// Ordinary play does not invalidate explicit consent to dissolve a running game.
	if _, err := svc.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Draw}); err != nil {
		t.Fatal(err)
	}
	missing := roomGuard(old)
	missing.ID = "missing"
	result, err := m.Delete(ctx, []room.DeleteTarget{roomGuard(old), missing})
	if err != nil || result.Deleted != 1 || result.Results[0].Status != "deleted" || result.Results[1].Status != "not_found" {
		t.Fatalf("partial result: %+v %v", result, err)
	}
	if _, err := svc.State(ctx, "table", a.Token); !errors.Is(err, room.ErrNotFound) {
		t.Fatal("deleted room still accessible", err)
	}
	var n int
	store.read.QueryRow(`SELECT count(*) FROM rooms WHERE id='table'`).Scan(&n)
	if n != 0 {
		t.Fatal("room was only soft deleted")
	}
	entries, _ := store.AuditLog(ctx, 10, 0)
	if len(entries.Entries) != 1 || entries.Entries[0].Action != "room.delete" || strings.Contains(entries.Entries[0].Detail, "hand") {
		t.Fatal("incorrect deletion audit", entries)
	}
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "table"}); err != nil {
		t.Fatal(err)
	}
	newRoom, _ := m.Inspect(ctx, "table")
	if newRoom.Instance == old.Instance {
		t.Fatal("reused storage instance")
	}
	// Match the new phase deliberately: instance identity alone must stop stale deletion.
	stale := roomGuard(old)
	stale.Phase = newRoom.State.Phase
	result, err = m.Delete(ctx, []room.DeleteTarget{stale})
	if err != nil || result.Results[0].Status != "changed" {
		t.Fatal("stale consent deleted recreated room", result, err)
	}
	store.Close()
	store = openTest(t, path, nil, 10)
	m = room.NewManager(serviceTest(t, store, nil), store)
	if _, err := m.Inspect(ctx, "table"); err != nil {
		t.Fatal("recreated room lost on restart", err)
	}
}

func TestDeleteRejectsStartedLobbyAndRollsBackOnAuditFailure(t *testing.T) {
	ctx := context.Background()
	store := openTest(t, filepath.Join(t.TempDir(), "guard.db"), nil, 10)
	svc := serviceTest(t, store, nil)
	svc.Create(ctx, room.CreateRequest{ID: "waiting"})
	a, _ := svc.Join(ctx, "waiting", "Alice")
	svc.Join(ctx, "waiting", "Bob")
	m := room.NewManager(svc, store)
	lobby, _ := m.Inspect(ctx, "waiting")
	svc.Execute(ctx, "waiting", a.Token, room.ActionRequest{Type: game.Start})
	result, err := m.Delete(ctx, []room.DeleteTarget{roomGuard(lobby)})
	if err != nil || result.Results[0].Status != "changed" {
		t.Fatal("started lobby was deleted", result, err)
	}
	active, _ := m.Inspect(ctx, "waiting")
	store.write.Exec(`CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	result, err = m.Delete(ctx, []room.DeleteTarget{roomGuard(active)})
	if err != nil || result.Results[0].Status != "failed" {
		t.Fatal("unexpected audit failure result", result, err)
	}
	if _, err := m.Inspect(ctx, "waiting"); err != nil {
		t.Fatal("audit failure deleted room", err)
	}
}

func TestBulkDeleteContinuesAfterOneAuditFailure(t *testing.T) {
	ctx := context.Background()
	store := openTest(t, filepath.Join(t.TempDir(), "partial-delete.db"), nil, 10)
	svc := serviceTest(t, store, nil)
	m := room.NewManager(svc, store)
	var targets []room.DeleteTarget
	for _, id := range []string{"reject", "allow"} {
		if _, err := svc.Create(ctx, room.CreateRequest{ID: id}); err != nil {
			t.Fatal(err)
		}
		v, err := m.Inspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, roomGuard(v))
	}
	if _, err := store.write.Exec(`CREATE TRIGGER reject_one_audit BEFORE INSERT ON admin_audit WHEN NEW.target='reject' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := m.Delete(ctx, targets)
	if err != nil || result.Deleted != 1 || result.Results[0].Status != "failed" || result.Results[1].Status != "deleted" {
		t.Fatal("batch stopped or reported incorrect results", result, err)
	}
	if _, err := m.Inspect(ctx, "reject"); err != nil {
		t.Fatal("failed item was deleted", err)
	}
	if _, err := m.Inspect(ctx, "allow"); !errors.Is(err, room.ErrNotFound) {
		t.Fatal("successful item survived", err)
	}
	entries, err := store.AuditLog(ctx, 10, 0)
	if err != nil || len(entries.Entries) != 1 || entries.Entries[0].Target != "allow" {
		t.Fatal("incorrect audit after partial failure", entries, err)
	}
}

func TestRoomDirectorySearchFiltersStatsAndActivity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := openTest(t, filepath.Join(t.TempDir(), "directory.db"), clock, 10)
	svc := serviceTest(t, store, clock)
	svc.Create(ctx, room.CreateRequest{ID: "one_room"})
	svc.Join(ctx, "one_room", "Alice")
	now = now.Add(time.Minute)
	svc.Create(ctx, room.CreateRequest{ID: "two-room"})
	svc.Join(ctx, "two-room", "Bob")
	m := room.NewManager(svc, store)
	page, err := m.List(ctx, room.RoomQuery{Limit: 1, Sort: "activity"})
	if err != nil || page.Total != 2 || page.Stats.Total != 2 || page.Stats.Lobby != 2 || page.Rooms[0].ID != "two-room" {
		t.Fatal("directory ordering/stats", page, err)
	}
	for _, query := range []string{"ALICE", "_", "%"} {
		page, err = m.List(ctx, room.RoomQuery{Limit: 20, Query: query})
		want := 1
		if query == "%" {
			want = 0
		}
		if err != nil || page.Total != want || page.Stats.Total != 2 {
			t.Fatal("literal name search", query, page, err)
		}
	}
	v, _ := m.Inspect(ctx, "one_room")
	if _, err := m.SetAccess(ctx, "one_room", room.AccessRequest{Instance: v.Instance, JoinLocked: true}); err != nil {
		t.Fatal(err)
	}
	page, err = m.List(ctx, room.RoomQuery{Limit: 20, Access: "locked", Kind: "human", Phase: game.Lobby})
	if err != nil || page.Total != 1 || !page.Rooms[0].JoinLocked || page.Rooms[0].HostName != "Alice" {
		t.Fatal("filters", page, err)
	}
	for _, query := range []room.RoomQuery{{Limit: 20, Sort: "SQL"}, {Limit: 20, Kind: "invalid"}, {Limit: 20, Phase: "invalid"}, {Limit: 20, Access: "invalid"}, {Limit: 20, Query: strings.Repeat("a", 65)}} {
		if _, err := m.List(ctx, query); err == nil {
			t.Fatal("invalid directory query accepted")
		}
	}
}

func TestJoinLockAndKickPreserveLifecycleAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "members.db")
	store := openTest(t, path, nil, 10)
	svc := serviceTest(t, store, nil)
	svc.Create(ctx, room.CreateRequest{ID: "members"})
	a, _ := svc.JoinAccount(ctx, "members", "alice", "Alice")
	b, _ := svc.JoinAccount(ctx, "members", "bob", "Bob")
	c, _ := svc.SpectateAccount(ctx, "members", "viewer", "Viewer")
	m := room.NewManager(svc, store)
	v, _ := m.Inspect(ctx, "members")
	locked, err := m.SetAccess(ctx, "members", room.AccessRequest{Instance: v.Instance, JoinLocked: true})
	if err != nil || !locked.JoinLocked {
		t.Fatal("lock failed", err)
	}
	if _, err := svc.JoinAccount(ctx, "members", "newcomer", "New"); err == nil {
		t.Fatal("new player bypassed lock")
	}
	if _, err := svc.SpectateAccount(ctx, "members", "newviewer", "New"); err == nil {
		t.Fatal("new spectator bypassed lock")
	}
	a, err = svc.JoinAccount(ctx, "members", "alice", "Alice")
	if err != nil {
		t.Fatal("lock blocked existing account recovery", err)
	}
	svc.Execute(ctx, "members", a.Token, room.ActionRequest{Type: game.Start})
	v, err = m.Kick(ctx, "members", a.PlayerID, v.Instance)
	if err != nil || v.HostID != b.PlayerID || v.State.Phase != game.Finished || len(v.Departed) != 1 {
		t.Fatal("kick broke forfeit/host transfer", v, err)
	}
	if _, err := svc.State(ctx, "members", a.Token); !errors.Is(err, room.ErrUnauthorized) {
		t.Fatal("kicked room credential survived", err)
	}
	if _, err := m.Kick(ctx, "members", c.PlayerID, v.Instance); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Kick(ctx, "members", b.PlayerID, v.Instance); err == nil {
		t.Fatal("removed last human instead of explicit dissolution")
	}
	store.Close()
	store = openTest(t, path, nil, 10)
	svc = serviceTest(t, store, nil)
	m = room.NewManager(svc, store)
	restored, err := m.Inspect(ctx, "members")
	if err != nil || !restored.JoinLocked || restored.HostID != b.PlayerID || len(restored.Spectators) != 0 {
		t.Fatal("room controls not durable", restored, err)
	}
	if _, err := svc.State(ctx, "members", b.Token); err != nil {
		t.Fatal("remaining member lost credential", err)
	}
}

func TestAdminControlsRollBackTogetherWithAudit(t *testing.T) {
	ctx := context.Background()
	store := openTest(t, filepath.Join(t.TempDir(), "control-audit.db"), nil, 10)
	svc := serviceTest(t, store, nil)
	a, _ := started(t, svc)
	m := room.NewManager(svc, store)
	before, _ := m.Inspect(ctx, "table")
	if _, err := store.write.Exec(`CREATE TRIGGER reject_admin_audit BEFORE INSERT ON admin_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAccess(ctx, "table", room.AccessRequest{Instance: before.Instance, JoinLocked: true}); err == nil {
		t.Fatal("lock succeeded without audit")
	}
	if _, err := m.Kick(ctx, "table", a.PlayerID, before.Instance); err == nil {
		t.Fatal("kick succeeded without audit")
	}
	after, err := m.Inspect(ctx, "table")
	if err != nil || after.JoinLocked || after.HostID != a.PlayerID || after.State.Version != before.State.Version || len(after.Departed) != 0 {
		t.Fatal("failed audit committed control changes", after, err)
	}
	if _, err := svc.State(ctx, "table", a.Token); err != nil {
		t.Fatal("failed kick revoked valid credential", err)
	}
}

func TestAdministrativeDeleteCannotBeResurrectedByInFlightWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-delete.db")
	writer := openTest(t, path, nil, 10)
	adminStore := openTest(t, path, nil, 10)
	svc := serviceTest(t, writer, nil)
	started(t, svc)
	m := room.NewManager(serviceTest(t, adminStore, nil), adminStore)
	before, _ := m.Inspect(ctx, "table")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- writer.Transact(ctx, "table", func(r *room.Record) error {
			r.Revision++
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	deleted, err := m.Delete(ctx, []room.DeleteTarget{roomGuard(before)})
	close(release)
	if err != nil || deleted.Deleted != 1 {
		t.Fatal("concurrent deletion failed", deleted, err)
	}
	if err := <-done; !errors.Is(err, room.ErrNotFound) {
		t.Fatal("in-flight writer resurrected room", err)
	}
	if _, err := m.Inspect(ctx, "table"); !errors.Is(err, room.ErrNotFound) {
		t.Fatal("deleted room was restored", err)
	}
}
