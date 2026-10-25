package game

// CardEdit belongs to a trusted administration boundary, never to player commands.
// A swap replaces a hand card with an existing deck/hand card. A move changes
// hand size; an empty destination returns the card to the bottom of the deck.
type CardEdit struct {
	Operation     string   `json:"operation"`
	CardID        int      `json:"card_id"`
	ReplacementID int      `json:"replacement_id,omitempty"`
	ToPlayerID    PlayerID `json:"to_player_id,omitempty"`
}

type cardLocation struct {
	pile          *[]Card
	index, player int
}

func editableCard(s *state, id int) (cardLocation, bool) {
	for i, card := range s.draw {
		if card.ID == id {
			return cardLocation{&s.draw, i, -1}, true
		}
	}
	for p := range s.players {
		if s.players[p].eliminated {
			continue
		}
		for i, card := range s.players[p].hand {
			if card.ID == id {
				return cardLocation{&s.players[p].hand, i, p}, true
			}
		}
	}
	return cardLocation{}, false
}

// EditCards is failure-atomic, preserves physical identities and historical
// challenge evidence, and invalidates in-flight player/computer decisions.
func (g *Game) EditCards(edit CardEdit, expectedVersion uint64) error {
	if expectedVersion != g.s.version {
		return fail(VersionConflict, "state version is stale")
	}
	if g.s.phase != Playing {
		return fail(InvalidState, "only a running game can be edited")
	}
	n := g.s.clone()
	source, ok := editableCard(&n, edit.CardID)
	if !ok {
		return fail(InvalidInput, "choose an active hand or draw-pile card")
	}
	switch edit.Operation {
	case "swap":
		if edit.ToPlayerID != "" {
			return fail(InvalidInput, "swap does not accept a destination player")
		}
		target, ok := editableCard(&n, edit.ReplacementID)
		if !ok || source.player == target.player {
			return fail(InvalidInput, "choose a replacement from a different active hand or the draw pile")
		}
		(*source.pile)[source.index], (*target.pile)[target.index] = (*target.pile)[target.index], (*source.pile)[source.index]
		if n.drawnCard == edit.CardID {
			n.drawnCard = edit.ReplacementID
		} else if n.drawnCard == edit.ReplacementID {
			n.drawnCard = edit.CardID
		}
	case "move":
		if edit.ReplacementID != 0 {
			return fail(InvalidInput, "move does not accept a replacement card")
		}
		if edit.CardID == n.drawnCard {
			return fail(InvalidState, "replace the just-drawn card instead of removing it before the turn ends")
		}
		destination := &n.draw
		target := -1
		if edit.ToPlayerID != "" {
			target = n.index(edit.ToPlayerID)
			if target < 0 || n.players[target].eliminated {
				return fail(InvalidInput, "destination must be an active player")
			}
			destination = &n.players[target].hand
		}
		if source.player == target {
			return fail(InvalidInput, "card is already in the destination")
		}
		if source.player >= 0 && len(*source.pile) <= 1 {
			return fail(InvalidState, "cannot remove the last card of an active player")
		}
		card := (*source.pile)[source.index]
		*source.pile = append((*source.pile)[:source.index], (*source.pile)[source.index+1:]...)
		*destination = append(*destination, card)
		if source.player >= 0 {
			n.cancelUNO(source.player)
		}
		if target >= 0 {
			n.cancelUNO(target)
		}
	default:
		return fail(InvalidInput, "operation must be swap or move")
	}
	if err := g.refillDrawPile(&n); err != nil {
		return err
	}
	n.version++
	if err := validateCheckpoint(n, g.maxPlayers); err != nil {
		return err
	}
	g.s = n
	return nil
}
