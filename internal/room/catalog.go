package room

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"unour/game"
)

type RoomQuery struct {
	Query  string
	Phase  game.Phase
	Kind   string // human or bot
	Access string // open or locked
	Sort   string // activity, oldest, created, members
	Limit  int
	Offset int
}

// Stats cover all rooms, independent of the current directory filter.
type RoomStats struct {
	Total    int `json:"total"`
	Lobby    int `json:"lobby"`
	Playing  int `json:"playing"`
	Finished int `json:"finished"`
	Humans   int `json:"humans"`
}

func (m *Manager) List(ctx context.Context, q RoomQuery) (RoomPage, error) {
	q.Query = strings.TrimSpace(q.Query)
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 1000000 ||
		!utf8.ValidString(q.Query) || utf8.RuneCountInString(q.Query) > 64 || strings.IndexFunc(q.Query, unicode.IsControl) >= 0 ||
		(q.Phase != "" && q.Phase != game.Lobby && q.Phase != game.Playing && q.Phase != game.Finished) ||
		(q.Kind != "" && q.Kind != "human" && q.Kind != "bot") ||
		(q.Access != "" && q.Access != "open" && q.Access != "locked") ||
		(q.Sort != "" && q.Sort != "activity" && q.Sort != "oldest" && q.Sort != "created" && q.Sort != "members") {
		return RoomPage{}, &Error{Code: "invalid_input", Message: "invalid room directory query"}
	}
	return m.store.ListRooms(ctx, q)
}
