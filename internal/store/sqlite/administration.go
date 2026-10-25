package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"unour/internal/account"
	"unour/internal/audit"
)

var _ account.ManagementStore = (*Store)(nil)

func (s *Store) ListAccounts(ctx context.Context, query string, limit, offset int, now time.Time) (account.UserPage, error) {
	page := account.UserPage{Users: []account.ManagedUser{}}
	query = "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE username_key LIKE ? ESCAPE '\'`, query).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.username,a.avatar_digest,a.created_at,a.disabled,(SELECT count(*) FROM account_sessions s WHERE s.account_id=a.id AND s.expires_at>?) FROM accounts a WHERE a.username_key LIKE ? ESCAPE '\' ORDER BY a.created_at DESC,a.id LIMIT ? OFFSET ?`, now.UnixMilli(), query, limit, offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var user account.ManagedUser
		var created int64
		var digest string
		if err := rows.Scan(&user.ID, &user.Username, &digest, &created, &user.Disabled, &user.ActiveSessions); err != nil {
			return page, err
		}
		user.CreatedAt = time.UnixMilli(created).UTC()
		if digest != "" {
			user.AvatarURL = "/api/v1/avatars/" + digest
		}
		page.Users = append(page.Users, user)
	}
	return page, rows.Err()
}
func (s *Store) ManageAccount(ctx context.Context, id string, change account.Change, at time.Time) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	detail := ""
	switch change.Kind {
	case "status":
		result, err = tx.ExecContext(ctx, `UPDATE accounts SET disabled=?,credential_version=credential_version+1 WHERE id=?`, change.Disabled, id)
		detail = "disabled=" + strconv.FormatBool(change.Disabled)
	case "password":
		result, err = tx.ExecContext(ctx, `UPDATE accounts SET password_hash=?,credential_version=credential_version+1 WHERE id=?`, change.PasswordHash, id)
	case "revoke":
		result, err = tx.ExecContext(ctx, `UPDATE accounts SET credential_version=credential_version+1 WHERE id=?`, id)
	default:
		return fmt.Errorf("unknown account management operation")
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return account.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_sessions WHERE account_id=?`, id); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, audit.Entry{At: at, Action: "user." + change.Kind, Target: id, Detail: detail}); err != nil {
		return err
	}
	return tx.Commit()
}
func insertAudit(ctx context.Context, tx *sql.Tx, entry audit.Entry) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_audit(at,action,target,detail) VALUES(?,?,?,?)`, entry.At.UnixMilli(), entry.Action, entry.Target, entry.Detail)
	return err
}
func (s *Store) AuditLog(ctx context.Context, limit, offset int) (audit.Page, error) {
	page := audit.Page{Entries: []audit.Entry{}}
	if limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		return page, &account.Error{Code: "invalid_input", Message: "invalid audit pagination"}
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_audit`).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,at,action,target,detail FROM admin_audit ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry audit.Entry
		var at int64
		if err := rows.Scan(&entry.ID, &at, &entry.Action, &entry.Target, &entry.Detail); err != nil {
			return page, err
		}
		entry.At = time.UnixMilli(at).UTC()
		page.Entries = append(page.Entries, entry)
	}
	return page, rows.Err()
}
