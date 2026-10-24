package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"unour/internal/account"
)

var _ account.Store = (*Store)(nil)

func (s *Store) CreateAccount(ctx context.Context, r account.Record, session account.SessionRecord) error {
	if session.AccountID != r.Profile.ID {
		return fmt.Errorf("initial session must belong to the new account")
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id,username,username_key,password_hash,created_at) VALUES(?,?,?,?,?)`, r.Profile.ID, r.Profile.Username, r.UsernameKey, r.PasswordHash, r.Profile.CreatedAt.UnixMilli())
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
		return account.ErrUsernameTaken
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_sessions(digest,account_id,created_at,expires_at) VALUES(?,?,?,?)`, session.Digest[:], session.AccountID, session.CreatedAt.UnixMilli(), session.ExpiresAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
func scanProfile(row interface{ Scan(...any) error }) (account.Profile, error) {
	var p account.Profile
	var digest string
	var created int64
	err := row.Scan(&p.ID, &p.Username, &digest, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, account.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.CreatedAt = time.UnixMilli(created).UTC()
	if digest != "" {
		p.AvatarURL = "/api/v1/avatars/" + digest
	}
	return p, nil
}
func (s *Store) AccountByName(ctx context.Context, key string) (account.Record, error) {
	var r account.Record
	var digest string
	var created int64
	err := s.read.QueryRowContext(ctx, `SELECT id,username,avatar_digest,created_at,username_key,password_hash FROM accounts WHERE username_key=?`, key).Scan(&r.Profile.ID, &r.Profile.Username, &digest, &created, &r.UsernameKey, &r.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return r, account.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Profile.CreatedAt = time.UnixMilli(created).UTC()
	if digest != "" {
		r.Profile.AvatarURL = "/api/v1/avatars/" + digest
	}
	return r, nil
}
func (s *Store) PublicProfiles(ctx context.Context, ids []string) (map[string]account.Profile, error) {
	result := make(map[string]account.Profile, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > 10 {
		return nil, fmt.Errorf("too many profile lookups")
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.read.QueryContext(ctx, `SELECT id,username,avatar_digest,created_at FROM accounts WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		result[p.ID] = p
	}
	return result, rows.Err()
}
func (s *Store) CreateAccountSession(ctx context.Context, r account.SessionRecord) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE expires_at<=?`, r.CreatedAt.UnixMilli()); err != nil {
		return err
	}
	// Retain at most ten active devices per account, including this login.
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE account_id=? AND digest IN (SELECT digest FROM account_sessions WHERE account_id=? ORDER BY created_at DESC,rowid DESC LIMIT -1 OFFSET 9)`, r.AccountID, r.AccountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_sessions(digest,account_id,created_at,expires_at) VALUES(?,?,?,?)`, r.Digest[:], r.AccountID, r.CreatedAt.UnixMilli(), r.ExpiresAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SessionAccount(ctx context.Context, digest [32]byte, now time.Time) (account.Profile, error) {
	p, err := scanProfile(s.read.QueryRowContext(ctx, `SELECT a.id,a.username,a.avatar_digest,a.created_at FROM account_sessions s JOIN accounts a ON a.id=s.account_id WHERE s.digest=? AND s.expires_at>?`, digest[:], now.UnixMilli()))
	if errors.Is(err, account.ErrNotFound) {
		return p, account.ErrUnauthorized
	}
	return p, err
}
func (s *Store) DeleteAccountSession(ctx context.Context, digest [32]byte) error {
	_, err := s.write.ExecContext(ctx, `DELETE FROM account_sessions WHERE digest=?`, digest[:])
	return err
}
func (s *Store) SetAccountAvatar(ctx context.Context, id, digest string, data []byte) (account.Profile, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return account.Profile{}, err
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRowContext(ctx, `SELECT avatar_digest FROM accounts WHERE id=?`, id).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return account.Profile{}, account.ErrNotFound
		}
		return account.Profile{}, err
	}
	if digest != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO avatars(digest,content) VALUES(?,?) ON CONFLICT(digest) DO NOTHING`, digest, data); err != nil {
			return account.Profile{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET avatar_digest=? WHERE id=?`, digest, id); err != nil {
		return account.Profile{}, err
	}
	if old != "" && old != digest {
		if _, err := tx.ExecContext(ctx, `DELETE FROM avatars WHERE digest=? AND NOT EXISTS(SELECT 1 FROM accounts WHERE avatar_digest=?)`, old, old); err != nil {
			return account.Profile{}, err
		}
	}
	p, err := scanProfile(tx.QueryRowContext(ctx, `SELECT id,username,avatar_digest,created_at FROM accounts WHERE id=?`, id))
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}
func (s *Store) AccountAvatar(ctx context.Context, digest string) ([]byte, error) {
	var data []byte
	err := s.read.QueryRowContext(ctx, `SELECT content FROM avatars WHERE digest=?`, digest).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, account.ErrAvatarNotFound
	}
	return data, err
}
