package game

import "testing"

func TestRematchIsAtomicAndKeepsVersionAcrossRounds(t *testing.T) {
	g, _ := New(Options{InitialHand: 1, Shuffle: func([]Card) error { return nil }})
	_ = g.Join("a", "Alice")
	_ = g.Join("b", "Bob")
	if err := g.Rematch([]PlayerIdentity{{ID: "a", Name: "Alice"}, {ID: "b", Name: "Bob"}}); err == nil {
		t.Fatal("rematch accepted before finished")
	}
	_, _ = g.Apply(Command{Type: Start, PlayerID: "a"})
	// A finished checkpoint with one empty hand and all physical cards retained.
	cp := g.Checkpoint()
	cp.Discard = append(cp.Discard, cp.Players[0].Hand...)
	cp.Players[0].Hand = nil
	cp.ActiveColor = cp.Discard[len(cp.Discard)-1].Color
	cp.Phase = Finished
	cp.Winner = "a"
	g, err := Restore(cp, g.rules, g.shuffle)
	if err != nil {
		t.Fatal(err)
	}
	version := g.View("").Version
	if err := g.Rematch([]PlayerIdentity{{ID: "a", Name: "Alice"}, {ID: "a", Name: "duplicate"}}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if g.View("").Phase != Finished || g.View("").Version != version {
		t.Fatal("failed reset mutated game")
	}
	if err := g.Rematch([]PlayerIdentity{{ID: "a", Name: "Alice"}, {ID: "b", Name: "Bob"}, {ID: "c", Name: "Carol"}}); err != nil {
		t.Fatal(err)
	}
	v := g.View("c")
	if v.Phase != Playing || v.Version <= version || len(v.Players) != 3 || len(v.Players[2].Hand) != 1 {
		t.Fatalf("bad new round: %+v", v)
	}
	if _, err := Restore(g.Checkpoint(), g.rules, g.shuffle); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(Command{Type: Draw, PlayerID: v.Turn, ExpectedVersion: &version}); err == nil {
		t.Fatal("old round command accepted")
	}
}

func TestLeavingAdvancesTurnAndSettlesWinner(t *testing.T) {
	g, _ := New(Options{Shuffle: func([]Card) error { return nil }})
	_ = g.Join("a", "Alice")
	_ = g.Join("b", "Bob")
	_ = g.Join("c", "Carol")
	if err := g.Leave("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(g.Checkpoint(), g.rules, g.shuffle); err != nil {
		t.Fatal(err)
	}
	_, _ = g.Apply(Command{Type: Start, PlayerID: "a"})
	if err := g.Leave("a"); err != nil {
		t.Fatal(err)
	}
	v := g.View("")
	if v.Phase != Finished || v.Winner != "c" {
		t.Fatalf("departure did not settle: %+v", v)
	}
	if _, err := Restore(g.Checkpoint(), g.rules, g.shuffle); err != nil {
		t.Fatal(err)
	}
}

func TestRematchExpandingRosterStillProducesRestorableVersion(t *testing.T) {
	g, _ := New(Options{InitialHand: 1, Shuffle: func([]Card) error { return nil }})
	_ = g.Join("a", "Alice")
	_ = g.Join("b", "Bob")
	_, _ = g.Apply(Command{Type: Start, PlayerID: "a"})
	_ = g.Leave("b")
	roster := []PlayerIdentity{{ID: "a", Name: "Alice"}}
	for _, id := range []PlayerID{"c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		roster = append(roster, PlayerIdentity{ID: id, Name: string(id)})
	}
	if err := g.Rematch(roster); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(g.Checkpoint(), g.rules, g.shuffle); err != nil {
		t.Fatal(err)
	}
}
