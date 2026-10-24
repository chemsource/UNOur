// Package game contains the UNO aggregate and its rule extension protocol.
// A Game has a single owner: callers must serialize access to it.
package game

import (
	"crypto/rand"
	"math/big"
)

type Color string

const (
	Red       Color = "red"
	Blue      Color = "blue"
	Green     Color = "green"
	Yellow    Color = "yellow"
	WildColor Color = "wild"
	KeepColor Color = "keep"
)

func (c Color) Valid() bool { return c == Red || c == Blue || c == Green || c == Yellow }

type Kind string

const (
	Number       Kind = "number"
	Skip         Kind = "skip"
	Reverse      Kind = "reverse"
	DrawTwo      Kind = "draw_two"
	Wild         Kind = "wild"
	WildDrawFour Kind = "wild_draw_four"
)

type Card struct {
	ID     int   `json:"id"`
	Color  Color `json:"color"`
	Kind   Kind  `json:"kind"`
	Number int   `json:"number,omitempty"`
}

func (c Card) IsWild() bool { return c.Kind == Wild || c.Kind == WildDrawFour }

// StandardDeck returns fresh physical cards. IDs never change during a game.
func StandardDeck() []Card {
	deck := make([]Card, 0, 108)
	add := func(color Color, kind Kind, number int) {
		deck = append(deck, Card{ID: len(deck) + 1, Color: color, Kind: kind, Number: number})
	}
	for _, color := range []Color{Red, Blue, Green, Yellow} {
		add(color, Number, 0)
		for n := 1; n <= 9; n++ {
			add(color, Number, n)
			add(color, Number, n)
		}
		for _, kind := range []Kind{Skip, Reverse, DrawTwo} {
			add(color, kind, 0)
			add(color, kind, 0)
		}
	}
	for i := 0; i < 4; i++ {
		add(WildColor, Wild, 0)
		add(WildColor, WildDrawFour, 0)
	}
	return deck
}

// ShuffleFunc must only reorder cards. An error aborts the entire command.
type ShuffleFunc func([]Card) error

func secureShuffle(cards []Card) error {
	for i := len(cards) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(n.Int64())
		cards[i], cards[j] = cards[j], cards[i]
	}
	return nil
}
