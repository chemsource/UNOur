// Package account owns account identities, credentials and profile use cases.
// It has no dependency on HTTP, rooms or the game rules.
package account

import (
	"context"
	"time"
)

type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

var (
	ErrUsernameTaken  = &Error{"username_taken", "username is already registered"}
	ErrCredentials    = &Error{"invalid_credentials", "username or password is incorrect"}
	ErrUnauthorized   = &Error{"account_unauthorized", "a valid account session is required"}
	ErrNotFound       = &Error{"account_not_found", "account does not exist"}
	ErrAvatarNotFound = &Error{"avatar_not_found", "avatar does not exist"}
	ErrAvatar         = &Error{"invalid_avatar", "upload a valid PNG or JPEG image, no larger than 4096 pixels per side"}
	ErrAvatarTooLarge = &Error{"avatar_too_large", "avatar must not exceed 2 MiB"}
	ErrAvatarBusy     = &Error{"avatar_busy", "avatar processing is busy; retry shortly"}
)

type Profile struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	AvatarURL string    `json:"avatar_url"`
	CreatedAt time.Time `json:"created_at"`
}

// Record is private storage data and must never be serialized to an API response.
type Record struct {
	Profile           Profile
	UsernameKey       string
	PasswordHash      string
	Disabled          bool
	CredentialVersion uint64
}
type SessionRecord struct {
	Digest               [32]byte
	AccountID            string
	CreatedAt, ExpiresAt time.Time
	CredentialVersion    uint64
}
type Store interface {
	// CreateAccount atomically persists the identity and its initial session.
	CreateAccount(context.Context, Record, SessionRecord) error
	AccountByName(context.Context, string) (Record, error)
	PublicProfiles(context.Context, []string) (map[string]Profile, error)
	CreateAccountSession(context.Context, SessionRecord) error
	SessionAccount(context.Context, [32]byte, time.Time) (Profile, error)
	DeleteAccountSession(context.Context, [32]byte) error
	SetAccountAvatar(context.Context, string, string, []byte) (Profile, error)
	AccountAvatar(context.Context, string) ([]byte, error)
}
