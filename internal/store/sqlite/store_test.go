package sqlite

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"unour/game"
	"unour/internal/room"
)

func openTest(t *testing.T, path string, now func() time.Time, max int) *Store {
	t.Helper()
	s, err := Open(Config{Path: path, MaxRooms: max, Now: now, Codec: room.NewCodec(testShuffle)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func emptyRecord(t *testing.T) *room.Record {
	t.Helper()
	g, err := game.New(game.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &room.Record{Game: g, Rules: game.DefaultRuleSpecs(), Sessions: map[[32]byte]game.PlayerID{}, CreatedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}
}
func testShuffle(cards []game.Card) error {
	front := []int{1, 26, 102, 27, 4}
	out := make([]game.Card, 0, len(cards))
	used := map[int]bool{}
	for _, id := range front {
		for _, card := range cards {
			if card.ID == id {
				out = append(out, card)
				used[id] = true
			}
		}
	}
	for _, c := range cards {
		if !used[c.ID] {
			out = append(out, c)
		}
	}
	copy(cards, out)
	return nil
}
func serviceTest(t *testing.T, s *Store, now func() time.Time) *room.Service {
	t.Helper()
	v, err := room.New(s, room.Config{Now: now, GameOptions: game.Options{InitialHand: 2, Shuffle: testShuffle}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func started(t *testing.T, s *room.Service) (room.JoinResult, room.JoinResult) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Create(ctx, room.CreateRequest{ID: "table"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Join(ctx, "table", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Join(ctx, "table", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Start}); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestRestartPreservesSessionsUNOAndDeduplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.db")
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ctx := context.Background()
	s := openTest(t, path, clock, 10)
	svc := serviceTest(t, s, clock)
	a, b := started(t, svc)
	played, err := svc.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Play, CardID: 1})
	if err != nil {
		t.Fatal(err)
	}
	w := played.Room.State.Players[0].UNO
	if w == nil {
		t.Fatal("missing UNO window")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path, clock, 10)
	// Deliberately use default game options after reopening: saved parameters win.
	svc, err = room.New(s, room.Config{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.State(ctx, "table", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view, played.Room) {
		t.Fatal("room changed after reopen")
	}
	now = now.Add(3 * time.Second)
	req := room.ActionRequest{Type: game.PenalizeUNO, WindowID: w.ID}
	first, err := svc.Execute(ctx, "table", a.Token, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Room.State.Players[0].HandCount != 2 || first.Room.State.Turn != b.PlayerID {
		t.Fatal("wrong restored penalty")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path, clock, 10)
	svc = serviceTest(t, s, clock)
	again, err := svc.Execute(ctx, "table", a.Token, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Room, first.Room) {
		t.Fatal("retry after restart applied penalty twice")
	}
}

func TestRestartPreservesChallengeEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.db")
	s := openTest(t, path, nil, 10)
	svc := serviceTest(t, s, nil)
	ctx := context.Background()
	a, b := started(t, svc)
	if _, err := svc.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Play, CardID: 102, Color: game.Blue}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path, nil, 10)
	svc = serviceTest(t, s, nil)
	result, err := svc.Execute(ctx, "table", b.Token, room.ActionRequest{Type: game.Challenge})
	if err != nil {
		t.Fatal(err)
	}
	if result.Room.State.Turn != b.PlayerID || result.Room.State.PendingDraw != 0 || result.Room.State.Players[0].HandCount != 5 {
		t.Fatal("restored challenge lost evidence")
	}
}

func TestFailedCallbackAndFailedCommitDoNotPersist(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "rooms.db"), nil, 10)
	ctx := context.Background()
	svc := serviceTest(t, s, nil)
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "table"}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Join(ctx, "table", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "table", "Bob"); err != nil {
		t.Fatal(err)
	}
	before, err := svc.State(ctx, "table", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("reject callback")
	if err := s.Transact(ctx, "table", func(r *room.Record) error { _ = r.Game.Join("uncommitted", "ghost"); return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	// A deferred foreign key fails specifically at COMMIT, after UPDATE succeeds.
	for _, sql := range []string{
		`CREATE TABLE commit_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE commit_guard (id INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER fail_commit AFTER UPDATE ON rooms BEGIN INSERT INTO commit_guard(id) VALUES(1); END`,
	} {
		if _, err := s.write.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Start}); err == nil {
		t.Fatal("commit failure was reported as success")
	}
	after, err := svc.State(ctx, "table", a.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed transaction left changed state")
	}
	if _, err := s.write.Exec(`DROP TRIGGER fail_commit`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, "table", a.Token, room.ActionRequest{Type: game.Start}); err != nil {
		t.Fatal("failed commit poisoned connection", err)
	}
}

func TestReadUsesCommittedSnapshotAndDoesNotWrite(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "rooms.db"), nil, 10)
	ctx := context.Background()
	if err := s.Create(ctx, "r", emptyRecord(t)); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := s.read.QueryRow(`SELECT revision FROM rooms WHERE id='r'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Transact(ctx, "r", func(r *room.Record) error {
			r.CreatedAt = r.CreatedAt.Add(time.Second)
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	readDone := make(chan error, 1)
	go func() { readDone <- s.Read(ctx, "r", func(r *room.Record) error { return nil }) }()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read was blocked by game callback")
	}
	var after int64
	if err := s.read.QueryRow(`SELECT revision FROM rooms WHERE id='r'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("state query wrote the room")
	}
}

func TestCASRejectsCrossStoreAndDeleteRecreateRaces(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "delete recreate"}[recreate], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rooms.db")
			a := openTest(t, path, nil, 10)
			b := openTest(t, path, nil, 10)
			ctx := context.Background()
			if err := a.Create(ctx, "r", emptyRecord(t)); err != nil {
				t.Fatal(err)
			}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				done <- a.Transact(ctx, "r", func(r *room.Record) error {
					r.CreatedAt = r.CreatedAt.Add(time.Second)
					close(entered)
					<-release
					return nil
				})
			}()
			<-entered
			if recreate {
				if err := b.Delete(ctx, "r", func(*room.Record) error { return nil }); err != nil {
					t.Fatal(err)
				}
				r := emptyRecord(t)
				r.CreatedAt = r.CreatedAt.Add(10 * time.Second)
				if err := b.Create(ctx, "r", r); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := b.Transact(ctx, "r", func(r *room.Record) error { r.CreatedAt = r.CreatedAt.Add(10 * time.Second); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if err := <-done; !errors.Is(err, room.ErrConflict) {
				t.Fatalf("stale state committed: %v", err)
			}
			if err := a.Read(ctx, "r", func(r *room.Record) error {
				if r.CreatedAt.Second() != 10 {
					t.Fatal("new record overwritten")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentJoinsCapacityDeletionAndRetention(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "rooms.db")
	s := openTest(t, path, clock, 1)
	svc := serviceTest(t, s, clock)
	ctx := context.Background()
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	joined := make(chan room.JoinResult, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := svc.Join(ctx, "r", "player"); err == nil {
				joined <- r
			}
		}()
	}
	wg.Wait()
	close(joined)
	if len(joined) != 10 {
		t.Fatalf("concurrent joins succeeded %d times", len(joined))
	}
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "overflow"}); !errors.Is(err, room.ErrCapacity) {
		t.Fatal("capacity lost", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path, clock, 1)
	svc = serviceTest(t, s, clock)
	now = now.Add(24 * time.Hour)
	if n, err := svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatal("default retention deleted persisted rooms", n, err)
	}
	n, err := s.Sweep(ctx, now.Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatal("explicit retention failed", n, err)
	}
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "new", func(*room.Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path, clock, 1)
	if err := s.Read(ctx, "new", func(*room.Record) error { return nil }); !errors.Is(err, room.ErrNotFound) {
		t.Fatal("deleted room resurrected", err)
	}
}

func TestFutureSchemaAndCorruptPayloadFailClosed(t *testing.T) {
	for _, future := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "rooms.db")
		s := openTest(t, path, nil, 1)
		if err := s.Create(context.Background(), "r", emptyRecord(t)); err != nil {
			t.Fatal(err)
		}
		statement := `UPDATE rooms SET payload='corrupt'`
		if future {
			statement = `PRAGMA user_version=999`
		}
		if _, err := s.write.Exec(statement); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		r, err := Open(Config{Path: path, MaxRooms: 1})
		if r != nil {
			_ = r.Close()
		}
		if err == nil {
			t.Fatal("unknown/corrupt database was silently reset")
		}
	}
}

func TestSnapshotNeverContainsRawSessionToken(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "rooms.db"), nil, 1)
	svc := serviceTest(t, s, nil)
	ctx := context.Background()
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Join(ctx, "r", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := s.read.QueryRow(`SELECT payload FROM rooms WHERE id='r'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, a.Token) {
		t.Fatal("database stores raw bearer token")
	}
}

func TestWaitingCancellationSweepAndIndependentRooms(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "rooms.db"), nil, 2)
	ctx := context.Background()
	for _, id := range []string{"busy", "other"} {
		if err := s.Create(ctx, id, emptyRecord(t)); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Transact(ctx, "busy", func(*room.Record) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := s.Transact(waitCtx, "busy", func(*room.Record) error { t.Error("cancelled callback ran"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting request ignored cancellation", err)
	}
	other := make(chan error, 1)
	go func() { other <- s.Transact(ctx, "other", func(*room.Record) error { return nil }) }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("one room blocked another room's commit")
	}
	n, err := s.Sweep(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatal("sweep did not skip active room", n, err)
	}
}

func TestReadPoolCannotMutateDatabase(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "rooms.db"), nil, 1)
	ctx := context.Background()
	if err := s.Create(ctx, "r", emptyRecord(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.read.ExecContext(ctx, `DELETE FROM rooms`); err == nil {
		t.Fatal("read pool accepted a write")
	}
	if err := s.Read(ctx, "r", func(*room.Record) error { return nil }); err != nil {
		t.Fatal("read pool changed data", err)
	}
}

func TestCorruptStoredDomainDataIsAnInternalError(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "rooms.db"), nil, 1)
	ctx := context.Background()
	if err := s.Create(ctx, "r", emptyRecord(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.Exec(`UPDATE rooms SET payload=json_set(payload,'$.game.format_version',999)`); err != nil {
		t.Fatal(err)
	}
	err := s.Read(ctx, "r", func(*room.Record) error { return nil })
	var domain *game.Error
	if !errors.As(err, &domain) || domain.Code != game.InternalFailure {
		t.Fatal("storage corruption was classified as client input", err)
	}
}

func TestExistingWALAndSHMBecomePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.db")
	s := openTest(t, path, nil, 1)
	if err := s.Create(context.Background(), "r", emptyRecord(t)); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0644); err != nil {
			t.Fatal(err)
		}
	}
	reopened := openTest(t, path, nil, 1)
	if err := reopened.Transact(context.Background(), "r", func(r *room.Record) error { r.CreatedAt = r.CreatedAt.Add(time.Second); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("private snapshot file %s has mode %o", suffix, info.Mode().Perm())
		}
	}
}

func TestInvalidStorageMetadataFailsStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.db")
	s := openTest(t, path, nil, 1)
	if err := s.Create(context.Background(), "r", emptyRecord(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.Exec(`UPDATE rooms SET revision=?`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{Path: path, MaxRooms: 1})
	if reopened != nil {
		_ = reopened.Close()
	}
	if err == nil {
		t.Fatal("invalid storage metadata was accepted at startup")
	}
}
