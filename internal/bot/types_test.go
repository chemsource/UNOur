package bot

import "testing"

func TestProfileValues(t *testing.T) {
	for _, d := range []Difficulty{Easy, Normal, Hard, Expert} {
		if !d.Valid() {
			t.Errorf("invalid difficulty %q", d)
		}
	}
	if Difficulty("").Valid() {
		t.Fatal("empty difficulty accepted")
	}
	for _, s := range []Style{Balanced, Aggressive, Cautious} {
		if !s.Valid() {
			t.Errorf("invalid style %q", s)
		}
	}
	if Style("unknown").Valid() {
		t.Fatal("unknown style accepted")
	}
}
