package game

import (
	"encoding/json"
	"testing"
	"time"
)

func declarationInJSON(t *testing.T, view View, id PlayerID) uint64 {
	t.Helper()
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var public struct {
		Players []struct {
			ID          PlayerID `json:"id"`
			UNODeclared uint64   `json:"uno_declared"`
		} `json:"players"`
	}
	if err := json.Unmarshal(data, &public); err != nil {
		t.Fatal(err)
	}
	for _, p := range public.Players {
		if p.ID == id {
			return p.UNODeclared
		}
	}
	t.Fatalf("missing player %s", id)
	return 0
}

// A poll may skip both the play that opened UNO and the declaration command.
// Every viewer must still receive the declaration after later actions/restart.
func TestUNODeclarationSurvivesSkippedSnapshotsAndRestart(t *testing.T) {
	g := checkpointGame(t, 2)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	w := g.View("a").Players[0].UNO
	command := Command{Type: SayUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(time.Second)}
	apply(t, g, command)
	version := g.View("").Version
	apply(t, g, command)
	if g.View("").Version != version {
		t.Fatal("duplicate declaration advanced the game")
	}
	apply(t, g, Command{Type: Draw, PlayerID: "b"})
	g = restore(t, g)
	for _, viewer := range []PlayerID{"a", "b", ""} {
		view := g.View(viewer)
		if got := declarationInJSON(t, view, "a"); got != w.ID {
			t.Fatalf("viewer %q cannot observe UNO: got %d, want window %d", viewer, got, w.ID)
		}
		if view.Players[0].UNO != nil {
			t.Fatal("resolved UNO window remained open")
		}
		if viewer != "a" && len(view.Players[0].Hand) != 0 {
			t.Fatal("UNO announcement exposed a private hand")
		}
	}
}

func TestMissingUNODoesNotPublishDeclaration(t *testing.T) {
	g := checkpointGame(t, 2)
	apply(t, g, Command{Type: Play, PlayerID: "a", CardID: 1})
	w := g.View("a").Players[0].UNO
	if got := declarationInJSON(t, g.View("b"), "a"); got != 0 {
		t.Fatal("pending window was announced as a declaration")
	}
	apply(t, g, Command{Type: PenalizeUNO, PlayerID: "a", WindowID: w.ID, At: testTime.Add(2 * time.Second)})
	if got := declarationInJSON(t, g.View("b"), "a"); got != 0 {
		t.Fatal("penalty was announced as a declaration")
	}
}
