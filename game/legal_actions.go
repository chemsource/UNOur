package game

// LegalAction exposes only an actor's commands, never hidden opponent cards or
// challenge evidence. The normal command endpoint still revalidates every write.
type LegalAction struct {
	Action Action `json:"action"`
	CardID int    `json:"card_id,omitempty"`
	Color  Color  `json:"color,omitempty"`
}

func (g *Game) LegalActions(viewer PlayerID) []LegalAction {
	result := make([]LegalAction, 0)
	if g.s.phase != Playing || g.s.players[g.s.turn].id != viewer {
		return result
	}
	add := func(action Action, id int, color Color) {
		if err := g.CanApply(Command{Type: action, PlayerID: viewer, CardID: id, Color: color}); err == nil {
			result = append(result, LegalAction{Action: action, CardID: id, Color: color})
		}
	}
	for _, card := range g.s.players[g.s.turn].hand {
		if card.IsWild() {
			for _, color := range []Color{Red, Yellow, Green, Blue} {
				add(Play, card.ID, color)
			}
		} else {
			add(Play, card.ID, "")
		}
	}
	add(Draw, 0, "")
	add(Pass, 0, "")
	add(Challenge, 0, "")
	return result
}
