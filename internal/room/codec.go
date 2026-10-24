package room

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"unour/game"
)

const maxSnapshotBytes = 1 << 20

// RecordCodec is shared by durable adapters. Implementations must support
// concurrent calls; Decode returns an exclusively owned Record, and Encode must
// not retain or mutate its input. Its output contains private data.
type RecordCodec interface {
	Encode(*Record) ([]byte, error)
	Decode([]byte) (*Record, error)
}

type codec struct {
	rules   RuleBuilder
	shuffle game.ShuffleFunc
}

func NewCodec(rules RuleBuilder, shuffle game.ShuffleFunc) RecordCodec {
	if rules == nil {
		rules = game.NewRegistry()
	}
	return &codec{rules: rules, shuffle: shuffle}
}

type recordSnapshot struct {
	FormatVersion int                      `json:"format_version"`
	Game          game.Checkpoint          `json:"game"`
	HostID        game.PlayerID            `json:"host_id"`
	Sessions      map[string]game.PlayerID `json:"session_digests"`
	Rules         []game.RuleSpec          `json:"rules"`
	CreatedAt     time.Time                `json:"created_at"`
}

func (c *codec) Encode(r *Record) ([]byte, error) {
	if r == nil || r.Game == nil {
		return nil, fmt.Errorf("cannot persist a room without a game")
	}
	s := recordSnapshot{FormatVersion: 1, Game: r.Game.Checkpoint(), HostID: r.HostID, Sessions: make(map[string]game.PlayerID, len(r.Sessions)), Rules: copySpecs(r.Rules), CreatedAt: r.CreatedAt}
	for digest, id := range r.Sessions {
		s.Sessions[hex.EncodeToString(digest[:])] = id
	}
	if err := validateRecord(s); err != nil {
		return nil, err
	}
	// Refuse records that this adapter could not restore after acknowledging a
	// commit, including a mismatched registry or changed rule specification.
	rules, err := c.rules.Build(s.Rules)
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
	if s.FormatVersion != 1 {
		return nil, fmt.Errorf("unsupported room checkpoint format %d", s.FormatVersion)
	}
	if err := validateRecord(s); err != nil {
		return nil, err
	}
	rules, err := c.rules.Build(s.Rules)
	if err != nil {
		return nil, fmt.Errorf("restore room rules: %w", err)
	}
	g, err := game.Restore(s.Game, rules, c.shuffle)
	if err != nil {
		return nil, fmt.Errorf("restore game: %w", err)
	}
	r := &Record{Game: g, HostID: s.HostID, Sessions: make(map[[32]byte]game.PlayerID, len(s.Sessions)), Rules: copySpecs(s.Rules), CreatedAt: s.CreatedAt}
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
	if len(players) != len(s.Sessions) {
		return fmt.Errorf("room session count differs from player count")
	}
	if (len(players) == 0 && s.HostID != "") || (len(players) > 0 && s.HostID != players[0].ID) {
		return fmt.Errorf("room host differs from first player")
	}
	ids := make(map[game.PlayerID]bool, len(players))
	for _, p := range players {
		ids[p.ID] = true
	}
	assigned := make(map[game.PlayerID]bool, len(players))
	hashes := make(map[[32]byte]bool, len(players))
	for text, id := range s.Sessions {
		raw, err := hex.DecodeString(text)
		if err != nil || len(raw) != 32 || !ids[id] || assigned[id] {
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
	return nil
}
