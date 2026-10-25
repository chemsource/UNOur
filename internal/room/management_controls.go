package room

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"unour/game"
	"unour/internal/audit"
)

const MaxDeleteBatch = 50

type DeleteTarget struct {
	ID       string     `json:"id"`
	Instance string     `json:"instance"`
	Phase    game.Phase `json:"phase"`
}
type DeleteResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type DeleteBatch struct {
	Results []DeleteResult `json:"results"`
	Deleted int            `json:"deleted"`
}
type AccessRequest struct {
	Instance   string `json:"instance"`
	JoinLocked bool   `json:"join_locked"`
}

func validInstance(instance string) bool {
	decoded, err := hex.DecodeString(instance)
	return err == nil && len(decoded) == 16 && instance == hex.EncodeToString(decoded)
}

// Delete validates the complete request before deleting anything. Individual
// room transactions allow an explicit partial result when one room changed or
// its audit could not be persisted. No game snapshots are kept in the audit.
func (m *Manager) Delete(ctx context.Context, targets []DeleteTarget) (DeleteBatch, error) {
	if len(targets) == 0 || len(targets) > MaxDeleteBatch {
		return DeleteBatch{}, &Error{Code: "invalid_input", Message: "select between 1 and 50 rooms"}
	}
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		if !validID(target.ID) || !validInstance(target.Instance) || seen[target.ID] ||
			(target.Phase != game.Lobby && target.Phase != game.Playing && target.Phase != game.Finished) {
			return DeleteBatch{}, &Error{Code: "invalid_input", Message: "invalid or duplicate room deletion target"}
		}
		seen[target.ID] = true
	}
	batch := DeleteBatch{Results: make([]DeleteResult, 0, len(targets))}
	for _, target := range targets {
		entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.delete", Target: target.ID, Detail: "phase=" + string(target.Phase)}
		err := m.store.DeleteManaged(ctx, target.ID, target.Instance, func(r *Record) error {
			if r.Game.View("").Phase != target.Phase {
				return ErrConflict
			}
			return nil
		}, entry)
		status := "failed"
		switch {
		case err == nil:
			status = "deleted"
			batch.Deleted++
		case errors.Is(err, ErrNotFound):
			status = "not_found"
		case errors.Is(err, ErrConflict):
			status = "changed"
		}
		batch.Results = append(batch.Results, DeleteResult{ID: target.ID, Status: status})
	}
	return batch, nil
}

func (m *Manager) SetAccess(ctx context.Context, id string, req AccessRequest) (Inspection, error) {
	if !validID(id) || !validInstance(req.Instance) {
		return Inspection{}, ErrInvalid
	}
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.access", Target: id, Detail: fmt.Sprintf("join_locked=%t", req.JoinLocked)}
	var result Inspection
	meta, err := m.store.TransactManaged(ctx, id, req.Instance, func(r *Record, meta ManagementMetadata) error {
		if r.JoinLocked != req.JoinLocked {
			r.JoinLocked = req.JoinLocked
			r.Revision++
		}
		result = inspection(id, r, meta)
		return nil
	}, entry)
	if err == nil {
		result.ManagementMetadata = meta
	}
	return result, err
}

func (m *Manager) Kick(ctx context.Context, id string, player game.PlayerID, instance string) (Inspection, error) {
	if !validID(id) || !validID(string(player)) || !validInstance(instance) {
		return Inspection{}, ErrInvalid
	}
	entry := audit.Entry{At: m.rooms.now().UTC(), Action: "room.kick", Target: id, Detail: "player=" + string(player)}
	var result Inspection
	meta, err := m.store.TransactManaged(ctx, id, instance, func(r *Record, meta ManagementMetadata) error {
		found := false
		for _, member := range r.Sessions {
			if member == player {
				found = true
			}
		}
		if !found {
			return &Error{Code: "member_not_found", Message: "human member is no longer in this room"}
		}
		if len(r.Sessions) <= 1 {
			return &Error{Code: "last_member", Message: "delete the room to remove its last human member"}
		}
		if err := m.rooms.removeMember(r, player); err != nil {
			return err
		}
		result = inspection(id, r, meta)
		return nil
	}, entry)
	if err == nil {
		result.ManagementMetadata = meta
	}
	return result, err
}
