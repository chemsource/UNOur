package room

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"unour/game"
	"unour/internal/bot"
)

const (
	maxSnapshotBytes     = 1 << 20
	recordSnapshotFormat = 2
)

// RecordCodec is shared by durable adapters. Implementations must support
// concurrent calls; Decode returns an exclusively owned Record, and Encode must
// not retain or mutate its input. Its output contains private data.
type RecordCodec interface {
	Encode(*Record) ([]byte, error)
	Decode([]byte) (*Record, error)
}

type codec struct {
	shuffle game.ShuffleFunc
}

func NewCodec(shuffle game.ShuffleFunc) RecordCodec {
	return &codec{shuffle: shuffle}
}

type recordSnapshot struct {
	JoinLocked    bool                          `json:"join_locked,omitempty"`
	RosterVersion uint64                        `json:"roster_version,omitempty"`
	Departed      map[game.PlayerID]bool        `json:"departed,omitempty"`
	Spectators    []Spectator                   `json:"spectators,omitempty"`
	RematchVotes  map[game.PlayerID]bool        `json:"rematch_votes,omitempty"`
	Round         uint64                        `json:"round,omitempty"`
	Revision      uint64                        `json:"revision,omitempty"`
	FormatVersion int                           `json:"format_version"`
	Game          game.Checkpoint               `json:"game"`
	HostID        game.PlayerID                 `json:"host_id"`
	Sessions      map[string]game.PlayerID      `json:"session_digests"`
	Accounts      map[string]game.PlayerID      `json:"accounts,omitempty"`
	Rules         []game.RuleSpec               `json:"rules"`
	CreatedAt     time.Time                     `json:"created_at"`
	Bots          map[game.PlayerID]bot.Profile `json:"bots,omitempty"`
	BotMemory     bot.Memory                    `json:"bot_memory,omitzero"`
	BotTurnAt     time.Time                     `json:"bot_turn_at,omitzero"`
	BotUNO        map[game.PlayerID]BotUNO      `json:"bot_uno,omitempty"`
}

func (c *codec) Encode(r *Record) ([]byte, error) {
	if r == nil || r.Game == nil {
		return nil, fmt.Errorf("cannot persist a room without a game")
	}
	s := recordSnapshot{FormatVersion: recordSnapshotFormat, Game: r.Game.Checkpoint(), HostID: r.HostID, Sessions: make(map[string]game.PlayerID, len(r.Sessions)), Accounts: r.Accounts, Rules: copySpecs(r.Rules), CreatedAt: r.CreatedAt}
	s.RosterVersion = r.RosterVersion
	s.JoinLocked = r.JoinLocked
	s.Departed = r.Departed
	s.Spectators, s.RematchVotes, s.Round, s.Revision = r.Spectators, r.RematchVotes, r.Round, r.Revision
	s.Bots, s.BotMemory, s.BotTurnAt, s.BotUNO = r.Bots, r.BotMemory, r.BotTurnAt, r.BotUNO
	for digest, id := range r.Sessions {
		s.Sessions[hex.EncodeToString(digest[:])] = id
	}
	if err := validateRecord(s); err != nil {
		return nil, err
	}
	// Refuse records that this adapter could not restore after acknowledging a
	// commit, including a mismatched registry or changed rule specification.
	rules, err := c.restoreRules(s.Game, s.Rules)
	if err != nil {
		return nil, fmt.Errorf("validate persisted rules: %w", err)
	}
	if _, err := game.Restore(s.Game, rules, c.shuffle); err != nil {
		return nil, fmt.Errorf("validate persisted game: %w", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encode room: %w", err)
	}
	if len(data) > maxSnapshotBytes {
		return nil, fmt.Errorf("room checkpoint exceeds 1 MiB")
	}
	return data, nil
}

func (c *codec) Decode(data []byte) (*Record, error) {
	if len(data) > maxSnapshotBytes {
		return nil, fmt.Errorf("room checkpoint exceeds 1 MiB")
	}
	var s recordSnapshot
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return nil, fmt.Errorf("decode room: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("room checkpoint has trailing data")
	}
	if s.FormatVersion != recordSnapshotFormat {
		return nil, fmt.Errorf("unsupported room checkpoint format %d", s.FormatVersion)
	}
	if err := validateRecord(s); err != nil {
		return nil, err
	}
	rules, err := c.restoreRules(s.Game, s.Rules)
	if err != nil {
		return nil, fmt.Errorf("restore room rules: %w", err)
	}
	g, err := game.Restore(s.Game, rules, c.shuffle)
	if err != nil {
		return nil, fmt.Errorf("restore game: %w", err)
	}
	r := &Record{Game: g, HostID: s.HostID, Sessions: make(map[[32]byte]game.PlayerID, len(s.Sessions)), Accounts: s.Accounts, Rules: copySpecs(s.Rules), CreatedAt: s.CreatedAt}
	r.RosterVersion = s.RosterVersion
	r.JoinLocked = s.JoinLocked
	r.Departed = s.Departed
	r.Spectators, r.RematchVotes, r.Round, r.Revision = s.Spectators, s.RematchVotes, s.Round, s.Revision
	r.Bots, r.BotMemory, r.BotTurnAt, r.BotUNO = s.Bots, s.BotMemory, s.BotTurnAt, s.BotUNO
	for text, id := range s.Sessions {
		raw, _ := hex.DecodeString(text)
		var digest [32]byte
		copy(digest[:], raw)
		r.Sessions[digest] = id
	}
	return r, nil
}

func validateRecord(s recordSnapshot) error {
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("room creation time is missing")
	}
	players := s.Game.Players
	if len(players)+len(s.Spectators)-len(s.Departed) != len(s.Sessions)+len(s.Bots) {
		return fmt.Errorf("room session count differs from player count")
	}
	ids := make(map[game.PlayerID]bool, len(players))
	for _, p := range players {
		ids[p.ID] = true
	}
	if len(s.Spectators) > 50 || s.Round == ^uint64(0) || s.Revision > ^uint64(0)-s.Game.Version {
		return fmt.Errorf("invalid room lifecycle counters")
	}
	queued := 0
	eligible := make(map[game.PlayerID]bool)
	for _, p := range players {
		eligible[p.ID] = !s.Departed[p.ID]
	}
	for _, p := range s.Spectators {
		if p.ID == "" || len(p.ID) > 64 || ids[p.ID] || p.Name == "" || p.Name != strings.TrimSpace(p.Name) || !utf8.ValidString(p.Name) || utf8.RuneCountInString(p.Name) > 64 || strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
			return fmt.Errorf("invalid spectator identity")
		}
		ids[p.ID] = true
		if p.NextRound {
			queued++
			eligible[p.ID] = true
		}
	}
	if len(players)-len(s.Departed)+queued > s.Game.MaxPlayers {
		return fmt.Errorf("next round exceeds capacity")
	}
	for id, agree := range s.RematchVotes {
		_, computer := s.Bots[id]
		if !agree || !eligible[id] || computer || s.Game.Phase != game.Finished {
			return fmt.Errorf("invalid rematch vote")
		}
	}
	for id, left := range s.Departed {
		_, computer := s.Bots[id]
		found := false
		for _, p := range players {
			if p.ID == id {
				found = true
				if s.Game.Phase == game.Playing && !p.Eliminated {
					return fmt.Errorf("departed active player")
				}
			}
		}
		if !left || !found || computer || s.Game.Phase == game.Lobby {
			return fmt.Errorf("invalid departed seat")
		}
	}
	assigned := make(map[game.PlayerID]bool, len(ids))
	accountSeats := make(map[game.PlayerID]bool, len(s.Accounts))
	for accountID, playerID := range s.Accounts {
		_, computer := s.Bots[playerID]
		if !validID(accountID) || !ids[playerID] || accountSeats[playerID] || computer || s.Departed[playerID] {
			return fmt.Errorf("invalid account seat mapping")
		}
		accountSeats[playerID] = true
	}
	hashes := make(map[[32]byte]bool, len(players))
	for text, id := range s.Sessions {
		raw, err := hex.DecodeString(text)
		_, computer := s.Bots[id]
		if err != nil || len(raw) != 32 || !ids[id] || assigned[id] || computer || s.Departed[id] {
			return fmt.Errorf("invalid room session mapping")
		}
		var digest [32]byte
		copy(digest[:], raw)
		if hashes[digest] {
			return fmt.Errorf("duplicate room session digest")
		}
		hashes[digest] = true
		assigned[id] = true
	}
	if len(s.Sessions) == 0 {
		if s.HostID != "" {
			return fmt.Errorf("host without session")
		}
	} else if !assigned[s.HostID] {
		return fmt.Errorf("host has no human session")
	}
	gameIDs := make(map[game.PlayerID]bool, len(players))
	for _, p := range players {
		gameIDs[p.ID] = true
	}
	if err := validateBots(s, gameIDs); err != nil {
		return err
	}
	return nil
}

func validateBots(s recordSnapshot, players map[game.PlayerID]bool) error {
	for id, profile := range s.Bots {
		if !players[id] || id == s.HostID || !profile.Difficulty.Valid() || !profile.Style.Valid() {
			return fmt.Errorf("invalid computer seat")
		}
	}
	if len(s.Bots) == 0 {
		if !s.BotTurnAt.IsZero() || len(s.BotUNO) > 0 || len(s.BotMemory.Discard) > 0 {
			return fmt.Errorf("computer state without computer players")
		}
		return nil
	}

	if len(s.BotMemory.Discard) != len(s.Game.Discard) {
		return fmt.Errorf("public discard memory differs from game")
	}
	for i, card := range s.BotMemory.Discard {
		if card != s.Game.Discard[i] {
			return fmt.Errorf("public discard memory contains a hidden or stale card")
		}
	}
	turn := game.PlayerID("")
	if s.Game.Phase == game.Playing {
		if s.Game.TurnIndex < 0 || s.Game.TurnIndex >= len(s.Game.Players) {
			return fmt.Errorf("invalid computer turn index")
		}
		turn = s.Game.Players[s.Game.TurnIndex].ID
	}
	_, computerTurn := s.Bots[turn]
	if computerTurn == s.BotTurnAt.IsZero() {
		return fmt.Errorf("computer turn deadline differs from current turn")
	}
	for id, task := range s.BotUNO {
		if _, ok := s.Bots[id]; !ok || task.At.IsZero() {
			return fmt.Errorf("invalid computer UNO task")
		}
		found := false
		for _, p := range s.Game.Players {
			if p.ID == id && p.UNO != nil && p.UNO.ID == task.WindowID {
				found = true
				if task.Declare != task.At.Before(p.UNO.Deadline) {
					return fmt.Errorf("UNO schedule contradicts declaration")
				}
			}
		}
		if !found {
			return fmt.Errorf("stale computer UNO task")
		}
	}
	for _, p := range s.Game.Players {
		if _, ok := s.Bots[p.ID]; ok && p.UNO != nil && s.BotUNO[p.ID].WindowID != p.UNO.ID {
			return fmt.Errorf("missing computer UNO task")
		}
	}
	return nil
}

// Room programs are reconstructed exclusively from their own definitions.
func (c *codec) restoreRules(cp game.Checkpoint, specs []game.RuleSpec) (game.RuleSet, error) {
	return game.BuildFrozenRules(cp.RuleModules, specs)
}
