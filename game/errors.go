package game

import "fmt"

type Code string

const (
	InvalidInput     Code = "invalid_input"
	InvalidState     Code = "invalid_state"
	NotYourTurn      Code = "not_your_turn"
	CardNotOwned     Code = "card_not_owned"
	CardNotPlayable  Code = "card_not_playable"
	RuleViolation    Code = "rule_violation"
	VersionConflict  Code = "version_conflict"
	UNOTooEarly      Code = "uno_too_early"
	UNOExpired       Code = "uno_expired"
	UNOWindowInvalid Code = "uno_window_invalid"
	InternalFailure  Code = "internal_error"
)

// Error is a stable domain failure; Cause is diagnostic and never part of JSON.
type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }
func fail(code Code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
