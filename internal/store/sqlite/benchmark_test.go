package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"unour/internal/room"
)

func benchmarkRoom(b *testing.B) (*Store, *room.Service, string) {
	b.Helper()
	s, err := Open(Config{Path: filepath.Join(b.TempDir(), "bench.db"), MaxRooms: 1})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	svc, err := room.New(s, room.Config{})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := svc.Create(ctx, room.CreateRequest{ID: "bench"}); err != nil {
		b.Fatal(err)
	}
	a, err := svc.Join(ctx, "bench", "Alice")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := svc.Join(ctx, "bench", "Bob"); err != nil {
		b.Fatal(err)
	}
	if _, err := svc.Execute(ctx, "bench", a.Token, room.ActionRequest{Type: "start"}); err != nil {
		b.Fatal(err)
	}
	return s, svc, a.Token
}

func BenchmarkState(b *testing.B) {
	_, svc, token := benchmarkRoom(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.State(ctx, "bench", token); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCommit(b *testing.B) {
	s, _, _ := benchmarkRoom(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Transact(ctx, "bench", func(r *room.Record) error { r.CreatedAt = r.CreatedAt.Add(time.Nanosecond); return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
