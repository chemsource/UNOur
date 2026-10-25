package game

import "time"

func (g *Game) validateAction(s *state, idx int, cmd Command, card Card, e Effects) error {
	switch cmd.Type {
	case Start:
		return g.validateStart(e)
	case Play:
		_, err := validatePlay(s, cmd, card, e)
		return err
	case Draw:
		return validateDrawTurn(s)
	case Pass:
		if s.drawnCard == 0 {
			return fail(InvalidState, "draw a card before passing")
		}
	case Challenge:
		return validateChallenge(s, idx, e)
	case SayUNO, PenalizeUNO:
		return validateUNO(s, idx, cmd)
	default:
		return fail(InvalidInput, "unknown action %q", cmd.Type)
	}
	return nil
}

func (g *Game) validateStart(e Effects) error {
	if e.HandLimit > 0 && g.initialHand >= e.HandLimit {
		return fail(InvalidInput, "hand limit must exceed the initial hand size")
	}
	return nil
}

func validatePlay(s *state, cmd Command, card Card, e Effects) (Color, error) {
	if s.pending > 0 && s.finishingPlayer() != "" {
		return "", fail(InvalidState, "the round is closing; settle the penalty or challenge instead of playing")
	}
	if s.drawnCard != 0 && s.drawnCard != cmd.CardID && !e.PlayAnyAfterDraw {
		return "", fail(CardNotPlayable, "only the card just drawn can be played")
	}
	if !e.Allow {
		return "", fail(CardNotPlayable, "card does not match or cannot answer the penalty")
	}
	chosen := s.active
	if card.IsWild() {
		if cmd.Color == KeepColor && e.KeepColor {
			chosen = s.active
		} else if cmd.Color.Valid() {
			chosen = cmd.Color
		} else {
			return "", fail(InvalidInput, "wild card requires a valid chosen color")
		}
		if chosen == s.active && !e.KeepColor {
			return "", fail(RuleViolation, "wild card must change the active color in this room")
		}
		if card.Kind == WildDrawFour && !e.AllowChallenge && (Context{s: s}).HasColor(cmd.PlayerID, s.active) {
			return "", fail(CardNotPlayable, "+4 cannot be played while holding the current color without the challenge rule")
		}
	} else {
		if cmd.Color != "" {
			return "", fail(InvalidInput, "a chosen color is only valid for a wild card")
		}
		chosen = card.Color
	}
	if s.pending+e.AddPenalty > 108 {
		return "", fail(RuleViolation, "penalty pool exceeds the deck size")
	}
	return chosen, nil
}

func validateDrawTurn(s *state) error {
	if s.pending == 0 && s.drawnCard != 0 {
		return fail(InvalidState, "already drew this turn; play that card or pass")
	}
	return nil
}

func validateChallenge(s *state, idx int, e Effects) error {
	if !e.AllowChallenge || s.challenge == nil || s.pending == 0 {
		return fail(InvalidState, "there is no +4 challenge available")
	}
	if s.index(s.challenge.offender) < 0 {
		return fail(InternalFailure, "challenge target is missing")
	}
	return nil
}

func validateUNO(s *state, idx int, cmd Command) error {
	p := &s.players[idx]
	if p.uno == nil || p.uno.ID != cmd.WindowID || cmd.WindowID == 0 {
		return fail(UNOWindowInvalid, "UNO window is stale or already resolved")
	}
	if cmd.Type == SayUNO {
		if !cmd.At.Before(p.uno.Deadline) {
			return fail(UNOExpired, "UNO declaration deadline has passed")
		}
		return nil
	}
	if cmd.Type != PenalizeUNO {
		return fail(InvalidInput, "unknown action %q", cmd.Type)
	}
	if cmd.At.Before(p.uno.Deadline) {
		return fail(UNOTooEarly, "UNO penalty is only valid after the two-second deadline")
	}
	if len(s.draw) == 0 && len(s.discard) <= 1 {
		return fail(InvalidState, "no physical card is currently available for the UNO penalty")
	}
	return nil
}

func (g *Game) start(s *state, e Effects, events *[]Event) error {
	if err := g.validateStart(e); err != nil {
		return err
	}
	deck := StandardDeck()
	if err := g.shuffle(deck); err != nil {
		return &Error{Code: InternalFailure, Message: "could not shuffle deck", Cause: err}
	}
	// Reserve the opening number before dealing. Prefer the undealt portion
	// to preserve the normal deal, but search the whole shuffled deck when a
	// large initial deal would otherwise consume every number card.
	dealCount := g.initialHand * len(s.players)
	opening := -1
	for offset := 0; offset < len(deck); offset++ {
		i := (dealCount + offset) % len(deck)
		if deck[i].Kind == Number {
			opening = i
			break
		}
	}
	if opening < 0 {
		return fail(InternalFailure, "deck has no number card for the opening discard")
	}
	s.discard = []Card{deck[opening]}
	s.active = deck[opening].Color
	deck = append(deck[:opening], deck[opening+1:]...)
	for round := 0; round < g.initialHand; round++ {
		for i := range s.players {
			s.players[i].hand = append(s.players[i].hand, deck[0])
			deck = deck[1:]
		}
	}
	s.draw = deck
	s.phase = Playing
	s.turn = 0
	*events = append(*events, Event{Type: "started"})
	return nil
}

func (g *Game) play(s *state, idx, cardIndex int, cmd Command, card Card, e Effects, events *[]Event) error {
	chosen, err := validatePlay(s, cmd, card, e)
	if err != nil {
		return err
	}
	var challenge *challengeWindow
	if card.Kind == WildDrawFour && e.AllowChallenge && e.AddPenalty > 0 {
		challenge = &challengeWindow{offender: cmd.PlayerID, hadColor: (Context{s: s}).HasColor(cmd.PlayerID, s.active)}
	}
	s.cancelUNO(idx)
	p := &s.players[idx]
	p.hand = append(p.hand[:cardIndex:cardIndex], p.hand[cardIndex+1:]...)
	s.discard = append(s.discard, card)
	s.active = chosen
	s.challenge = challenge
	if e.AddPenalty > 0 {
		s.pending += e.AddPenalty
		s.pendingKind = card.Kind
	}
	if e.Reverse {
		s.direction = -s.direction
	}
	if e.DrawNext > 0 {
		victim := s.next(idx, 1)
		if _, err := g.drawCards(s, victim, e.DrawNext, events); err != nil {
			return err
		}
	}
	s.advance(e.TurnSteps)
	*events = append(*events, Event{Type: "played", PlayerID: cmd.PlayerID})
	if len(p.hand) == 1 {
		p.uno = &UNOWindow{ID: s.version + 1, Deadline: cmd.At.Add(2 * time.Second)}
	}
	return nil
}

// drawCards moves existing cards only. When all recyclable cards are exhausted,
// the player draws the available amount; a penalty still settles and play moves
// on. This avoids creating duplicates or deadlocking an exhausted physical deck.
func (g *Game) drawCards(s *state, idx, count int, events *[]Event) (int, error) {
	actual := 0
	for actual < count {
		if len(s.draw) == 0 {
			if err := g.refillDrawPile(s); err != nil {
				return 0, err
			}
			if len(s.draw) == 0 {
				break
			}
		}
		s.cancelUNO(idx)
		s.players[idx].hand = append(s.players[idx].hand, s.draw[0])
		s.draw = s.draw[1:]
		actual++
	}
	*events = append(*events, Event{Type: "drew", PlayerID: s.players[idx].id, Count: actual})
	return actual, nil
}

// refillDrawPile preserves the exposed top and its chosen color. Only existing
// discard cards are shuffled; hands and physical card identities never change.
func (g *Game) refillDrawPile(s *state) error {
	if len(s.draw) > 0 || len(s.discard) <= 1 {
		return nil
	}
	top := s.discard[len(s.discard)-1]
	recycled := append([]Card(nil), s.discard[:len(s.discard)-1]...)
	if err := g.shuffle(recycled); err != nil {
		return &Error{Code: InternalFailure, Message: "could not recycle discard pile", Cause: err}
	}
	s.draw = recycled
	s.discard = []Card{top}
	return nil
}

func (g *Game) drawTurn(s *state, idx int, e Effects, events *[]Event) error {
	if err := validateDrawTurn(s); err != nil {
		return err
	}
	if s.pending > 0 {
		if _, err := g.drawCards(s, idx, s.pending, events); err != nil {
			return err
		}
		s.pending = 0
		s.pendingKind = ""
		s.challenge = nil
		s.advance(1)
		return nil
	}
	n, err := g.drawCards(s, idx, 1, events)
	if err != nil {
		return err
	}
	s.challenge = nil
	if n == 1 && e.PlayDrawnCard {
		s.drawnCard = s.players[idx].hand[len(s.players[idx].hand)-1].ID
	} else {
		s.advance(1)
	}
	return nil
}

func (g *Game) challenge(s *state, idx int, e Effects, events *[]Event) error {
	if err := validateChallenge(s, idx, e); err != nil {
		return err
	}
	target := s.index(s.challenge.offender)
	if s.challenge.hadColor {
		if _, err := g.drawCards(s, target, s.pending, events); err != nil {
			return err
		}
		*events = append(*events, Event{Type: "challenge_succeeded", PlayerID: s.players[idx].id})
	} else {
		penalty := (s.pending*e.ChallengeMultiplier + 99) / 100
		if _, err := g.drawCards(s, idx, penalty, events); err != nil {
			return err
		}
		*events = append(*events, Event{Type: "challenge_failed", PlayerID: s.players[idx].id})
		s.advance(1)
	}
	s.pending = 0
	s.pendingKind = ""
	s.challenge = nil
	s.drawnCard = 0
	return nil
}

func (g *Game) uno(s *state, idx int, cmd Command, events *[]Event) error {
	if err := validateUNO(s, idx, cmd); err != nil {
		return err
	}
	p := &s.players[idx]
	windowID := p.uno.ID
	if cmd.Type == SayUNO {
		*events = append(*events, Event{Type: "uno_declared", PlayerID: p.id})
	} else {
		n, err := g.drawCards(s, idx, 1, events)
		if err != nil {
			return err
		}
		if n != 1 {
			return fail(InvalidState, "no physical card is currently available for the UNO penalty")
		}
		*events = append(*events, Event{Type: "uno_penalized", PlayerID: p.id, Count: 1})
	}
	p.uno = nil
	p.lastUNO = windowID
	p.lastUNOAction = cmd.Type
	return nil
}
