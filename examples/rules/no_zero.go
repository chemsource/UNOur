// Package rules demonstrates a rule extension that uses only game's public API.
package rules

import (
	"bytes"
	"encoding/json"
	"fmt"

	"unour/game"
)

// NoZero is an example house rule. It adds a veto without changing matching,
// turn progression, HTTP handlers, or storage.
type NoZero struct{}

func (NoZero) ID() string { return "no_zero" }
func (NoZero) Evaluate(ctx game.Context, _ *game.Effects) error {
	if ctx.Action() == game.Play && ctx.Card().Kind == game.Number && ctx.Card().Number == 0 {
		return fmt.Errorf("zero cards are disabled in this room")
	}
	return nil
}

func Register(registry *game.Registry) error {
	return registry.Register("no_zero", func(options json.RawMessage) (game.Rule, error) {
		// This rule has no options. Reject configuration typos explicitly.
		if len(options) > 0 && !bytes.Equal(bytes.TrimSpace(options), []byte("{}")) {
			return nil, fmt.Errorf("no_zero accepts no options")
		}
		return NoZero{}, nil
	})
}
