package game

// Clone detaches all mutable state while sharing immutable rules and the shuffle
// callback. Callers must provide an independent shuffle callback when creating
// independent simulations; cloning a live game does not replace its randomness.
func (g *Game) Clone() *Game {
	n := *g
	n.s = g.s.clone()
	return &n
}

// Branch applies a command to a detached branch through the same failure-atomic
// engine as Apply. Unchanged card storage may be shared internally; future
// commands copy on write. Like Clone, Branch shares the shuffle callback: use a
// simulation-owned shuffle when branching simulations, never a live game's RNG.
func (g *Game) Branch(cmd Command) (*Game, Result, error) {
	n := *g
	result, err := n.Apply(cmd)
	if err != nil {
		return nil, Result{}, err
	}
	return &n, result, nil
}

// fork copies mutable player records and limits card slice capacity so appends
// allocate before writing shared storage. Card values, UNO windows and challenge
// records are immutable after construction; mutations replace their references.
// Removing a hand card must also cap the prefix before appending (see play).
func (s state) fork() state {
	n := s
	n.players = append([]player(nil), s.players...)
	for i := range n.players {
		n.players[i].hand = n.players[i].hand[:len(n.players[i].hand):len(n.players[i].hand)]
	}
	n.draw = n.draw[:len(n.draw):len(n.draw)]
	n.discard = n.discard[:len(n.discard):len(n.discard)]
	return n
}

// forkOwnedPlay prepares a validated play that cannot fail after mutation.
// Its caller must own the state without aliases; keeping the card capacities
// lets the play reuse hand and discard storage before replacing the state.
func (s state) forkOwnedPlay() state {
	n := s
	n.players = append([]player(nil), s.players...)
	return n
}
