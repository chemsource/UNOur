package game

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func assertCards(t testing.TB, g *Game) {
	t.Helper()
	seen := make(map[int]bool, 108)
	count := 0
	check := func(cards []Card) {
		for _, c := range cards {
			count++
			if c.ID < 1 || c.ID > 108 || seen[c.ID] {
				t.Fatalf("invalid/duplicate physical card ID %d", c.ID)
			}
			seen[c.ID] = true
			if c.IsWild() && c.Color != WildColor {
				t.Fatalf("wild identity changed: %+v", c)
			}
		}
	}
	check(g.s.draw)
	check(g.s.discard)
	for _, p := range g.s.players {
		check(p.hand)
	}
	if count != 108 {
		t.Fatalf("lost or created cards: %d", count)
	}
	if g.s.phase == Playing && (g.s.turn < 0 || g.s.turn >= len(g.s.players) || g.s.players[g.s.turn].eliminated) {
		t.Fatal("turn points to an invalid/eliminated player")
	}
	if _, err := Restore(g.Checkpoint(), g.rules, nil); err != nil {
		t.Fatalf("legal game cannot be persisted and restored: %v", err)
	}
}

func shuffledGame(t testing.TB, seed int64, extra ...Rule) *Game {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	rules, err := NewRegistry().Build(DefaultRuleSpecs())
	if err != nil {
		t.Fatal(err)
	}
	rules, err = NewRuleSet(append(rules.Rules(), extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Rules: rules, Shuffle: func(cards []Card) error {
		rng.Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []PlayerID{"a", "b", "c"} {
		if err := g.Join(id, string(id)); err != nil {
			t.Fatal(err)
		}
	}
	apply(t, g, Command{Type: Start, PlayerID: "a"})
	return g
}

func TestCompleteGamesConserveCards(t *testing.T) {
	for seed := int64(0); seed < 24; seed++ {
		g := shuffledGame(t, seed)
		for step := 0; step < 5000 && g.s.phase == Playing; step++ {
			assertCards(t, g)
			id := g.s.players[g.s.turn].id
			// Attempt actual commands; legality is never duplicated in the bot.
			played := false
			for _, c := range g.s.players[g.s.turn].hand {
				cmd := Command{Type: Play, PlayerID: id, CardID: c.ID, At: testTime.Add(time.Duration(step) * time.Second)}
				if c.IsWild() {
					cmd.Color = Red
				}
				if _, err := g.Apply(cmd); err == nil {
					played = true
					break
				}
			}
			if !played {
				action := Draw
				if g.s.drawnCard != 0 {
					action = Pass
				}
				apply(t, g, Command{Type: action, PlayerID: id})
			}
		}
		assertCards(t, g)
		if g.s.phase != Finished || g.s.winner == "" {
			t.Fatalf("seed %d did not complete", seed)
		}
	}
}

func FuzzCommandsPreserveCards(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	f.Add([]byte{255, 255, 0, 12, 48, 66, 78, 90, 101, 25, 12, 33})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 512 {
			input = input[:512]
		}
		var extra []Rule
		if len(input) > 0 && input[0]&1 != 0 {
			extra = append(extra, StackingRule{Mixed: true, DifferentColors: true, Reverse: true})
		}
		if len(input) > 0 && input[0]&2 != 0 {
			extra = append(extra, HandLimitRule{Limit: 12})
		}
		g := shuffledGame(t, 42, extra...)
		assertCards(t, g)
		for step, b := range input {
			if g.s.phase != Playing {
				break
			}
			actor := g.s.players[g.s.turn]
			c := Command{PlayerID: actor.id, At: testTime.Add(time.Duration(step) * time.Second)}
			switch b % 7 {
			case 0, 1:
				c.Type = Play
				if len(actor.hand) > 0 {
					card := actor.hand[int(b)%len(actor.hand)]
					c.CardID = card.ID
					if card.IsWild() {
						c.Color = []Color{Red, Blue, Green, Yellow}[int(b)%4]
					}
				}
			case 2:
				c.Type = Draw
			case 3:
				c.Type = Pass
			case 4:
				c.Type = Challenge
			case 5, 6:
				p := g.s.players[int(b)%len(g.s.players)]
				c.PlayerID = p.id
				c.Type = SayUNO
				if b%7 == 6 {
					c.Type = PenalizeUNO
				}
				if p.uno != nil {
					c.WindowID = p.uno.ID
				}
			}
			before := g.s.clone()
			_, err := g.Apply(c)
			if err != nil && !reflect.DeepEqual(before, g.s) {
				t.Fatalf("%s failed but changed state: %v", c.Type, err)
			}
			assertCards(t, g)
		}
	})
}

func BenchmarkPlay(b *testing.B) {
	g := shuffledGame(b, 42)
	base := g.s
	var command Command
	for _, c := range base.players[0].hand {
		command = Command{Type: Play, PlayerID: "a", CardID: c.ID, At: testTime}
		if c.IsWild() {
			command.Color = Red
		}
		if _, err := g.Apply(command); err == nil {
			break
		}
	}
	g.s = base
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.s = base
		if _, err := g.Apply(command); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkDraw(b *testing.B) {
	g := shuffledGame(b, 42)
	base := g.s
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.s = base
		if _, err := g.Apply(Command{Type: Draw, PlayerID: "a", At: testTime}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkPrivateView(b *testing.B) {
	g := shuffledGame(b, 42)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.View("a")
	}
}
