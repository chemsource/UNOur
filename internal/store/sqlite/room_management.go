package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"unour/internal/room"
)

var _ room.AdministrationStore = (*Store)(nil)

func (s *Store) ReadManaged(ctx context.Context, id string, fn func(*room.Record, room.ManagementMetadata) error) error {
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(r.record, room.ManagementMetadata{Instance: hex.EncodeToString(r.generation), UpdatedAt: time.UnixMilli(r.touched).UTC()})
}

// Read only public metadata in the SQLite engine; private hands and credential
// material never leave the database as part of the directory. Counts and rows
// use a single read snapshot, without restoring every game's rule engine.
const roomDirectory = `WITH directory AS (
 SELECT id,lower(hex(generation)) AS instance,touched_at,
 json_extract(p,'$.created_at') AS created_at,
 json_extract(p,'$.game.phase') AS phase,
 json_array_length(json_extract(p,'$.game.players')) - (SELECT count(*) FROM json_each(p,'$.departed')) AS players,
 (SELECT count(*) FROM json_each(p,'$.spectators')) AS spectators,
 (SELECT count(*) FROM json_each(p,'$.bots')) AS bots,
 (SELECT count(*) FROM json_each(p,'$.session_digests')) AS humans,
 max(coalesce(json_extract(p,'$.round'),1),1) AS round,
 coalesce(json_extract(p,'$.revision'),0)+json_extract(p,'$.game.version') AS revision,
 coalesce(json_extract(p,'$.join_locked'),0) AS join_locked,
 coalesce((SELECT json_extract(value,'$.name') FROM json_each(p,'$.game.players') WHERE json_extract(value,'$.id')=json_extract(p,'$.host_id')),
          (SELECT json_extract(value,'$.name') FROM json_each(p,'$.spectators') WHERE json_extract(value,'$.id')=json_extract(p,'$.host_id')),'') AS host_name,
 p
 FROM (SELECT id,generation,touched_at,CAST(payload AS TEXT) AS p FROM rooms)
) `

func (s *Store) ListRooms(ctx context.Context, q room.RoomQuery) (room.RoomPage, error) {
	page := room.RoomPage{Rooms: []room.RoomSummary{}}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, roomDirectory+`SELECT count(*),coalesce(sum(phase='lobby'),0),coalesce(sum(phase='playing'),0),coalesce(sum(phase='finished'),0),coalesce(sum(humans),0) FROM directory`).Scan(&page.Stats.Total, &page.Stats.Lobby, &page.Stats.Playing, &page.Stats.Finished, &page.Stats.Humans); err != nil {
		return page, err
	}
	where := " WHERE 1=1"
	args := []any{}
	if q.Query != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Query) + "%"
		where += ` AND (id LIKE ? ESCAPE '\' OR EXISTS(SELECT 1 FROM json_each(p,'$.game.players') member WHERE json_extract(member.value,'$.name') LIKE ? ESCAPE '\' AND NOT EXISTS(SELECT 1 FROM json_each(p,'$.departed') departed WHERE departed.key=json_extract(member.value,'$.id'))) OR EXISTS(SELECT 1 FROM json_each(p,'$.spectators') WHERE json_extract(value,'$.name') LIKE ? ESCAPE '\'))`
		args = append(args, pattern, pattern, pattern)
	}
	if q.Phase != "" {
		where += " AND phase=?"
		args = append(args, q.Phase)
	}
	if q.Kind == "human" {
		where += " AND bots=0"
	}
	if q.Kind == "bot" {
		where += " AND bots>0"
	}
	if q.Access != "" {
		where += " AND join_locked=?"
		args = append(args, q.Access == "locked")
	}
	if err := tx.QueryRowContext(ctx, roomDirectory+"SELECT count(*) FROM directory"+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	order := "touched_at DESC,id"
	switch q.Sort {
	case "oldest":
		order = "touched_at ASC,id"
	case "created":
		order = "julianday(created_at) DESC,id"
	case "members":
		order = "humans+bots DESC,touched_at DESC,id"
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := tx.QueryContext(ctx, roomDirectory+`SELECT id,instance,touched_at,created_at,phase,players,spectators,bots,humans,round,revision,join_locked,host_name FROM directory`+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item room.RoomSummary
		var touched int64
		var created string
		if err := rows.Scan(&item.ID, &item.Instance, &touched, &created, &item.Phase, &item.Players, &item.Spectators, &item.Bots, &item.Humans, &item.Round, &item.Revision, &item.JoinLocked, &item.HostName); err != nil {
			return page, err
		}
		item.UpdatedAt = time.UnixMilli(touched).UTC()
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return page, err
		}
		page.Rooms = append(page.Rooms, item)
	}
	return page, rows.Err()
}
