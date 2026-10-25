package account

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

type ManagedUser struct {
	Profile
	Disabled       bool `json:"disabled"`
	ActiveSessions int  `json:"active_sessions"`
}
type UserPage struct {
	Users []ManagedUser `json:"users"`
	Total int           `json:"total"`
}

// Change is internal storage data; passwords and hashes never enter an audit payload.
type Change struct {
	Kind         string
	Disabled     bool
	PasswordHash string
}
type ManagementStore interface {
	ListAccounts(context.Context, string, int, int, time.Time) (UserPage, error)
	ManageAccount(context.Context, string, Change, time.Time) error
}
type Manager struct{ store ManagementStore }

func NewManager(store ManagementStore) *Manager { return &Manager{store: store} }
func (m *Manager) List(ctx context.Context, query string, limit, offset int) (UserPage, error) {
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > 96 || limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		return UserPage{}, &Error{"invalid_input", "invalid user search or pagination"}
	}
	return m.store.ListAccounts(ctx, strings.ToLower(strings.TrimSpace(query)), limit, offset, time.Now().UTC())
}
func (m *Manager) SetDisabled(ctx context.Context, id string, disabled bool) error {
	return m.store.ManageAccount(ctx, id, Change{Kind: "status", Disabled: disabled}, time.Now().UTC())
}
func (m *Manager) RevokeSessions(ctx context.Context, id string) error {
	return m.store.ManageAccount(ctx, id, Change{Kind: "revoke"}, time.Now().UTC())
}
func (m *Manager) ResetPassword(ctx context.Context, id, password string) error {
	if !validPassword(password) {
		return &Error{"invalid_password", "password must contain 8–14 characters"}
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return m.store.ManageAccount(ctx, id, Change{Kind: "password", PasswordHash: hash}, time.Now().UTC())
}
