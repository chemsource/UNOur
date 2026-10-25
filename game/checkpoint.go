package game

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const checkpointFormat = 1

// Checkpoint contains private game data for trusted persistence adapters only.
// It must never be used as a player-facing response; use View for that purpose.
type Checkpoint struct {
	FormatVersion int                  `json:"format_version"`
	Version       uint64               `json:"version"`
	Phase         Phase                `json:"phase"`
	Players       []PlayerCheckpoint   `json:"players"`
	Deck          []Card               `json:"deck"`
	Discard       []Card               `json:"discard"`
	TurnIndex     int                  `json:"turn_index"`
	Direction     int                  `json:"direction"`
	ActiveColor   Color                `json:"active_color"`
	PendingDraw   int                  `json:"pending_draw"`
	PendingKind   Kind                 `json:"pending_kind"`
	Challenge     *ChallengeCheckpoint `json:"challenge,omitempty"`
	DrawnCardID   int                  `json:"drawn_card_id"`
	Winner        PlayerID             `json:"winner"`
	MaxPlayers    int                  `json:"max_players"`
	InitialHand   int                  `json:"initial_hand"`
	RuleModules   []RuleModule         `json:"rule_modules,omitempty"`
	RuleIDs       []string             `json:"rule_ids"`
}

type PlayerCheckpoint struct {
	ID            PlayerID   `json:"id"`
	Name          string     `json:"name"`
	Hand          []Card     `json:"hand"`
	Eliminated    bool       `json:"eliminated"`
	UNO           *UNOWindow `json:"uno,omitempty"`
	LastUNO       uint64     `json:"last_uno"`
	LastUNOAction Action     `json:"last_uno_action"`
}

type ChallengeCheckpoint struct {
	Offender PlayerID `json:"offender"`
	HadColor bool     `json:"had_color"`
}

// Checkpoint returns detached data, including all state needed for replay-safe
// UNO penalties and challenges. Rules include their frozen, data-only module definitions.
func (g *Game) Checkpoint() Checkpoint {
	s := g.s.clone()
	cp := Checkpoint{FormatVersion: checkpointFormat, Version: s.version, Phase: s.phase, Deck: s.draw, Discard: s.discard, TurnIndex: s.turn, Direction: s.direction, ActiveColor: s.active, PendingDraw: s.pending, PendingKind: s.pendingKind, DrawnCardID: s.drawnCard, Winner: s.winner, MaxPlayers: g.maxPlayers, InitialHand: g.initialHand, Players: make([]PlayerCheckpoint, 0, len(s.players)), RuleIDs: make([]string, 0, len(g.rules.rules))}
	cp.RuleModules = g.RuleModules()
	for _, p := range s.players {
		cp.Players = append(cp.Players, PlayerCheckpoint{ID: p.id, Name: p.name, Hand: p.hand, Eliminated: p.eliminated, UNO: p.uno, LastUNO: p.lastUNO, LastUNOAction: p.lastUNOAction})
	}
	for _, rule := range g.rules.rules {
		cp.RuleIDs = append(cp.RuleIDs, rule.ID())
	}
	if s.challenge != nil {
		cp.Challenge = &ChallengeCheckpoint{Offender: s.challenge.offender, HadColor: s.challenge.hadColor}
	}
	return cp
}

// Restore validates untrusted stored data before constructing a live aggregate.
// Shuffle is only used for future recycling; existing deck order is preserved.
func Restore(cp Checkpoint, rules RuleSet, shuffle ShuffleFunc) (*Game, error) {
	if cp.FormatVersion != checkpointFormat {
		return nil, fail(InvalidInput, "unsupported game checkpoint format %d", cp.FormatVersion)
	}
	if len(cp.RuleIDs) != len(rules.rules) || len(rules.rules) == 0 {
		return nil, fail(InvalidInput, "checkpoint rule set differs from installed rules")
	}
	for i, rule := range rules.rules {
		if cp.RuleIDs[i] != rule.ID() {
			return nil, fail(InvalidInput, "checkpoint rule order or ID differs from installed rules")
		}
	}
	g, err := New(Options{Rules: rules, Shuffle: shuffle, MaxPlayers: cp.MaxPlayers, InitialHand: cp.InitialHand})
	if err != nil {
		return nil, err
	}
	// New supplies defaults for zero options; a persisted checkpoint must be explicit.
	if cp.MaxPlayers != g.maxPlayers || cp.InitialHand != g.initialHand {
		return nil, fail(InvalidInput, "checkpoint is missing game parameters")
	}
	s := state{phase: cp.Phase, version: cp.Version, draw: cp.Deck, discard: cp.Discard, turn: cp.TurnIndex, direction: cp.Direction, active: cp.ActiveColor, pending: cp.PendingDraw, pendingKind: cp.PendingKind, drawnCard: cp.DrawnCardID, winner: cp.Winner}
	for _, p := range cp.Players {
		s.players = append(s.players, player{id: p.ID, name: p.Name, hand: p.Hand, eliminated: p.Eliminated, uno: p.UNO, lastUNO: p.LastUNO, lastUNOAction: p.LastUNOAction})
	}
	if cp.Challenge != nil {
		s.challenge = &challengeWindow{offender: cp.Challenge.Offender, hadColor: cp.Challenge.HadColor}
	}
	if err := validateCheckpoint(s, g.maxPlayers); err != nil {
		return nil, err
	}
	g.s = s.clone()
	return g, nil
}

func validateCheckpoint(s state, maxPlayers int) error {
	bad := func(message string) error { return fail(InvalidInput, "invalid game checkpoint: %s", message) }
	if s.phase != Lobby && s.phase != Playing && s.phase != Finished {
		return bad("unknown phase")
	}
	if len(s.players) > maxPlayers || s.version < uint64(len(s.players)) || s.version == ^uint64(0) {
		return bad("player count or version")
	}
	if s.direction != 1 && s.direction != -1 {
		return bad("direction")
	}
	if s.turn < 0 || (len(s.players) > 0 && s.turn >= len(s.players)) || (len(s.players) == 0 && s.turn != 0) {
		return bad("turn index")
	}
	ids := make(map[PlayerID]bool, len(s.players))
	windows := map[uint64]bool{}
	alive := 0
	for _, p := range s.players {
		if p.id == "" || len(p.id) > 64 || ids[p.id] || p.name == "" || p.name != strings.TrimSpace(p.name) || !utf8.ValidString(p.name) || utf8.RuneCountInString(p.name) > 64 || strings.IndexFunc(p.name, unicode.IsControl) >= 0 {
			return bad("player identity or name")
		}
		ids[p.id] = true
		if !p.eliminated {
			alive++
		}
		if p.lastUNO > s.version || (p.lastUNO == 0 && p.lastUNOAction != "") || (p.lastUNO > 0 && p.lastUNOAction != SayUNO && p.lastUNOAction != PenalizeUNO && p.lastUNOAction != "cancelled") {
			return bad("UNO deduplication record")
		}
		if p.uno != nil {
			if s.phase != Playing || p.eliminated || len(p.hand) != 1 || p.uno.ID == 0 || p.uno.ID > s.version || p.uno.ID <= p.lastUNO || p.uno.Deadline.IsZero() || windows[p.uno.ID] {
				return bad("UNO window")
			}
			windows[p.uno.ID] = true
		}
	}
	if s.pending < 0 || s.pending > 108 {
		return bad("penalty pool")
	}
	if s.pending == 0 && (s.pendingKind != "" || s.challenge != nil) {
		return bad("orphan penalty metadata")
	}
	if s.pending > 0 && s.pendingKind != Number && s.pendingKind != Skip && s.pendingKind != Reverse && s.pendingKind != DrawTwo && s.pendingKind != Wild && s.pendingKind != WildDrawFour {
		return bad("penalty kind")
	}
	if s.phase == Lobby {
		if s.version < uint64(len(s.players)) || s.direction != 1 || s.turn != 0 || s.active != "" || s.pending != 0 || s.drawnCard != 0 || s.winner != "" || len(s.draw) > 0 || len(s.discard) > 0 {
			return bad("lobby contains running-game state")
		}
		for _, p := range s.players {
			if len(p.hand) > 0 || p.eliminated || p.lastUNO != 0 {
				return bad("lobby player contains running-game state")
			}
		}
		return nil
	}
	if len(s.players) < 2 || s.version <= uint64(len(s.players)) || len(s.discard) == 0 || !s.active.Valid() {
		return bad("running game structure")
	}
	expected := StandardDeck()
	seen := make([]bool, len(expected)+1)
	count := 0
	check := func(cards []Card) bool {
		for _, card := range cards {
			if card.ID < 1 || card.ID > len(expected) || seen[card.ID] || expected[card.ID-1] != card {
				return false
			}
			seen[card.ID] = true
			count++
		}
		return true
	}
	if !check(s.draw) || !check(s.discard) {
		return bad("deck or discard identity")
	}
	for _, p := range s.players {
		if !check(p.hand) {
			return bad("hand identity")
		}
	}
	if count != len(expected) {
		return bad("physical cards are missing")
	}
	top := s.discard[len(s.discard)-1]
	if !top.IsWild() && s.active != top.Color {
		return bad("active color differs from colored discard")
	}
	if s.challenge != nil && (!ids[s.challenge.offender] || s.pendingKind != WildDrawFour || top.Kind != WildDrawFour) {
		return bad("challenge window")
	}
	if s.drawnCard != 0 {
		if s.phase != Playing || s.pending != 0 {
			return bad("draw restriction outside an ordinary turn")
		}
		found := false
		for _, card := range s.players[s.turn].hand {
			if card.ID == s.drawnCard {
				found = true
			}
		}
		if !found {
			return bad("draw restriction refers to a card not held")
		}
	}
	if s.phase == Playing {
		if alive < 2 || s.players[s.turn].eliminated || s.winner != "" {
			return bad("active turn or winner")
		}
		for _, p := range s.players {
			if !p.eliminated && len(p.hand) == 0 && s.pending == 0 {
				return bad("unsettled winner")
			}
		}
	} else {
		if s.challenge != nil || s.drawnCard != 0 {
			return bad("finished game contains active action metadata")
		}
		if s.winner == "" {
			if alive != 0 {
				return bad("missing winner")
			}
		} else {
			i := s.index(s.winner)
			if i < 0 || s.players[i].eliminated || (alive > 1 && (len(s.players[i].hand) != 0 || s.pending != 0)) {
				return bad("winner")
			}
		}
	}
	return nil
}
