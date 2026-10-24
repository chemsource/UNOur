package game

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func checkpointGame(t testing.TB, hand int) *Game {
	t.Helper()
	front := []int{1, 26, 102, 27, 4}
	if hand == 1 {
		front = []int{1, 26, 4}
	}
	g, err := New(Options{InitialHand: hand, Shuffle: func(cards []Card) error {
		ordered := make([]Card, 0, len(cards))
		used := map[int]bool{}
		for _, id := range front {
			ordered = append(ordered, cards[id-1])
			used[id] = true
		}
		for _, c := range cards {
			if !used[c.ID] {
				ordered = append(ordered, c)
			}
		}
		copy(cards, ordered)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Join("a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := g.Join("b", "Bob"); err != nil {
		t.Fatal(err)
	}
	apply(t, g, Command{Type: Start, PlayerID: "a"})
	return g
}

func restore(t testing.TB, g *Game) *Game {
	t.Helper()
	data, err := json.Marshal(g.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(cp, g.rules, func([]Card) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return restored
}

func TestCheckpointRestoresChallengeEvidenceAndAllViews(t *testing.T) {
	g := checkpointGame(t, 2)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 102, Color: Blue})
	r := restore(t, g)
	for _, id := range []PlayerID{"a", "b", ""} {
		if !reflect.DeepEqual(g.View(id), r.View(id)) {
			t.Fatalf("view changed for %q", id)
		}
	}
	if !reflect.DeepEqual(g.Checkpoint(), r.Checkpoint()) {
		t.Fatal("private checkpoint did not round trip")
	}
	apply(t, r, Command{Type: Challenge, PlayerID: "b"})
	if v := r.View("a"); v.Turn != "b" || v.PendingDraw != 0 || v.Players[0].HandCount != 5 {
		t.Fatalf("challenge evidence was lost: %+v", v)
	}
	assertCards(t, r)
}

func TestCheckpointRetainsUNODeadlineAndDeduplication(t *testing.T) {
	g := checkpointGame(t, 2)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	w := g.View("a").Players[0].UNO
	r := restore(t, g)
	c := Command{Type: PenalizeUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(2 * time.Second)}
	apply(t, r, c)
	r = restore(t, r)
	before := r.View("a")
	apply(t, r, c)
	if !reflect.DeepEqual(before, r.View("a")) || before.Players[0].HandCount != 2 || before.Turn != "b" {
		t.Fatal("UNO retry after restore was applied twice")
	}
	assertCards(t, r)
}

func TestCheckpointRestoresDrawRestrictionLobbyAndFinished(t *testing.T) {
	g := checkpointGame(t, 2)
	apply(t, g, Command{Type: Draw, PlayerID: "a"})
	r := restore(t, g)
	if _, err := r.Apply(Command{Type: Play, PlayerID: "a", CardID: 1, At: testTime}); err == nil {
		t.Fatal("restored draw restriction was lost")
	}
	apply(t, r, Command{Type: Pass, PlayerID: "a"})
	lobby, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := lobby.Join("a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lobby.Checkpoint(), restore(t, lobby).Checkpoint()) {
		t.Fatal("lobby changed")
	}
	g = checkpointGame(t, 1)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	r = restore(t, g)
	if r.View("a").Phase != Finished || r.View("a").Winner != "a" {
		t.Fatal("finished game lost winner")
	}
}

func TestCheckpointRejectsCorruption(t *testing.T) {
	g := checkpointGame(t, 2)
	for name, mutate := range map[string]func(*Checkpoint){
		"future format":         func(c *Checkpoint) { c.FormatVersion++ },
		"duplicate card":        func(c *Checkpoint) { c.Deck[0] = c.Players[0].Hand[0] },
		"missing card":          func(c *Checkpoint) { c.Deck = c.Deck[1:] },
		"changed physical card": func(c *Checkpoint) { c.Deck[0].Color = "purple" },
		"invalid turn":          func(c *Checkpoint) { c.TurnIndex = 999 },
		"duplicate identity":    func(c *Checkpoint) { c.Players[1].ID = c.Players[0].ID },
		"invalid color":         func(c *Checkpoint) { c.ActiveColor = WildColor },
		"negative pool":         func(c *Checkpoint) { c.PendingDraw = -1 },
		"invalid phase":         func(c *Checkpoint) { c.Phase = "broken" },
		"drawn card not owned":  func(c *Checkpoint) { c.DrawnCardID = 999 },
		"invalid winner":        func(c *Checkpoint) { c.Winner = "missing" },
		"missing rule":          func(c *Checkpoint) { c.RuleIDs[0] = "missing" },
		"invalid UNO":           func(c *Checkpoint) { c.Players[0].UNO = &UNOWindow{ID: c.Version + 1, Deadline: testTime} },
	} {
		t.Run(name, func(t *testing.T) {
			cp := g.Checkpoint()
			mutate(&cp)
			if _, err := Restore(cp, g.rules, nil); err == nil {
				t.Fatal("corrupt checkpoint accepted")
			}
		})
	}
}

func TestCheckpointCopiesDoNotAlias(t *testing.T) {
	g := checkpointGame(t, 2)
	cp := g.Checkpoint()
	r, err := Restore(cp, g.rules, nil)
	if err != nil {
		t.Fatal(err)
	}
	cp.Players[0].Hand[0].ID = 999
	cp.Deck[0].ID = 999
	cp.RuleIDs[0] = "bad"
	if g.View("a").Players[0].Hand[0].ID != 1 || r.View("a").Players[0].Hand[0].ID != 1 {
		t.Fatal("checkpoint aliases live state")
	}
}
