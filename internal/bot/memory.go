package bot

import (
	"math"
	"unour/game"
)

func (m Memory) Clone() Memory {
	m.Discard = append([]game.Card(nil), m.Discard...)
	m.Beliefs = append([]ColorBelief(nil), m.Beliefs...)
	return m
}

// Observe uses public card movements and events only, even if a caller supplied
// a player view containing a hand. It must be called after each successful action.
func (m *Memory) Observe(before, after game.View, cmd game.Command, events []game.Event) {
	started, played := false, false
	for _, event := range events {
		started = started || event.Type == "started"
		played = played || event.Type == "played"
	}
	if started {
		*m = Memory{}
	}
	for i := range m.Beliefs {
		m.Beliefs[i].Weight *= .9
	}
	if after.TopCard == nil {
		m.Discard = nil
		m.ChallengeColor = ""
		return
	}
	if len(m.Discard) == 0 && before.TopCard != nil && !started {
		m.Discard = []game.Card{*before.TopCard}
	}
	if played {
		m.Discard = append(m.Discard, *after.TopCard)
	}
	// Recycling removes every previous discard except the current top. This also
	// handles skip-draw recycling during a play, before the played event is emitted.
	if started || after.DiscardCount < before.DiscardCount || after.DiscardCount < len(m.Discard) || len(m.Discard) == 0 {
		m.Discard = []game.Card{*after.TopCard}
	}
	if played && after.TopCard.IsWild() {
		m.addBelief(cmd.PlayerID, after.ActiveColor, .55)
	}
	for _, event := range events {
		if event.Type == "drew" && event.Count > 0 {
			// New unknown cards weaken old evidence. A voluntary draw is never proof of
			// having no matching color; weak negative evidence retains full support.
			for i := range m.Beliefs {
				if m.Beliefs[i].PlayerID == event.PlayerID {
					m.Beliefs[i].Weight *= .65
				}
			}
			if cmd.Type == game.Draw && before.PendingDraw == 0 && event.PlayerID == cmd.PlayerID {
				m.addBelief(event.PlayerID, before.ActiveColor, -.4)
			}
		}
	}
	if after.ChallengeTarget == "" {
		m.ChallengeColor = ""
	} else if played && after.TopCard.Kind == game.WildDrawFour {
		m.ChallengeColor = before.ActiveColor
	}
}

func (m *Memory) addBelief(id game.PlayerID, color game.Color, delta float64) {
	if !color.Valid() {
		return
	}
	for i := range m.Beliefs {
		if m.Beliefs[i].PlayerID == id && m.Beliefs[i].Color == color {
			m.Beliefs[i].Weight = math.Max(-1.2, math.Min(1.2, m.Beliefs[i].Weight+delta))
			return
		}
	}
	m.Beliefs = append(m.Beliefs, ColorBelief{PlayerID: id, Color: color, Weight: delta})
}
