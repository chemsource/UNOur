package game

type PlayerView struct {
	ID         PlayerID   `json:"id"`
	Name       string     `json:"name"`
	HandCount  int        `json:"hand_count"`
	Hand       []Card     `json:"hand,omitempty"`
	Eliminated bool       `json:"eliminated"`
	UNO        *UNOWindow `json:"uno,omitempty"`
}

type View struct {
	Phase           Phase        `json:"phase"`
	Version         uint64       `json:"version"`
	You             PlayerID     `json:"you,omitempty"`
	Players         []PlayerView `json:"players"`
	Turn            PlayerID     `json:"turn,omitempty"`
	Direction       int          `json:"direction"`
	TopCard         *Card        `json:"top_card,omitempty"`
	ActiveColor     Color        `json:"active_color,omitempty"`
	DrawPileCount   int          `json:"draw_pile_count"`
	DiscardCount    int          `json:"discard_count"`
	PendingDraw     int          `json:"pending_draw"`
	PendingDrawKind Kind         `json:"pending_draw_kind,omitempty"`
	ChallengeTarget PlayerID     `json:"challenge_target,omitempty"`
	DrawnCardID     int          `json:"drawn_card_id,omitempty"`
	CanPass         bool         `json:"can_pass"`
	Winner          PlayerID     `json:"winner,omitempty"`
}

// View returns detached data. Neither opponents' hands nor challenge evidence
// can be obtained by marshaling this value, including after the game finishes.
func (g *Game) View(viewer PlayerID) View {
	s := &g.s
	v := View{Phase: s.phase, Version: s.version, You: viewer, Direction: s.direction, ActiveColor: s.active, DrawPileCount: len(s.draw), DiscardCount: len(s.discard), PendingDraw: s.pending, PendingDrawKind: s.pendingKind, Winner: s.winner, Players: make([]PlayerView, 0, len(s.players))}
	for _, p := range s.players {
		pv := PlayerView{ID: p.id, Name: p.name, HandCount: len(p.hand), Eliminated: p.eliminated}
		if p.id == viewer {
			pv.Hand = append([]Card(nil), p.hand...)
		}
		if p.uno != nil {
			u := *p.uno
			pv.UNO = &u
		}
		v.Players = append(v.Players, pv)
	}
	if s.phase == Playing {
		v.Turn = s.players[s.turn].id
	}
	if len(s.discard) > 0 {
		c := s.discard[len(s.discard)-1]
		v.TopCard = &c
	}
	if s.challenge != nil && s.phase == Playing {
		v.ChallengeTarget = s.challenge.offender
	}
	if s.phase == Playing && v.Turn == viewer {
		v.DrawnCardID = s.drawnCard
		v.CanPass = s.drawnCard != 0
	}
	return v
}
