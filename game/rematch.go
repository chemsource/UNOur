package game

// PlayerIdentity is a seat in the next round, without cards or turn state.
type PlayerIdentity struct {
	ID   PlayerID
	Name string
}

// Rematch atomically replaces a finished round. Versions remain monotonic so
// expected versions and UNO window IDs from previous rounds remain distinct.
func (g *Game) Rematch(players []PlayerIdentity) error {
	if g.s.phase != Finished {
		return fail(InvalidState, "only a finished game can be restarted")
	}
	if g.s.version > ^uint64(0)-2 {
		return fail(InvalidState, "game version exhausted")
	}
	next, err := New(Options{Rules: g.rules, Shuffle: g.shuffle, MaxPlayers: g.maxPlayers, InitialHand: g.initialHand})
	if err != nil {
		return err
	}
	for _, p := range players {
		if err := next.Join(p.ID, p.Name); err != nil {
			return err
		}
	}
	if len(players) < 2 {
		return fail(InvalidState, "at least two players are required")
	}
	next.s.version = max(g.s.version+1, uint64(len(players)))
	if _, err := next.Apply(Command{Type: Start, PlayerID: players[0].ID}); err != nil {
		return err
	}
	g.s = next.s
	return nil
}

// Leave removes a lobby seat or forfeits a running seat. A forfeited hand stays
// in the aggregate so physical card conservation and challenge evidence hold.
func (g *Game) Leave(id PlayerID) error {
	idx := g.s.index(id)
	if idx < 0 {
		return fail(InvalidInput, "player is not in the game")
	}
	if g.s.phase == Finished {
		return nil
	}
	n := g.s.clone()
	if n.phase == Lobby {
		n.players = append(n.players[:idx], n.players[idx+1:]...)
	} else {
		n.players[idx].eliminated = true
		n.cancelUNO(idx)
		if n.turn == idx {
			n.advance(1)
		}
		events := []Event{}
		n.settle(&events)
	}
	n.version++
	g.s = n
	return nil
}
