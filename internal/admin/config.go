// Package admin owns privileged credentials and sessions, independently of players.
package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"unicode/utf8"

	"unour/internal/credential"
)

// Config contains a salted hash, never a plaintext password. Do not log it.
type Config struct {
	PasswordHash string `json:"password_hash"`
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	before, err := os.Lstat(path)
	if err != nil {
		return cfg, fmt.Errorf("read admin configuration: %w", err)
	}
	if !before.Mode().IsRegular() {
		return cfg, fmt.Errorf("admin configuration must be a regular file, not a symlink")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return cfg, fmt.Errorf("open admin configuration: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return cfg, err
	}
	permissions := info.Mode().Perm()
	owner, owned := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !os.SameFile(before, info) || (permissions != 0600 && permissions != 0400) || info.Size() > 8192 || !owned || (owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) {
		return cfg, fmt.Errorf("admin configuration must be a private regular file (0600 or 0400), at most 8 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid admin configuration JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF || !credential.ValidHash(cfg.PasswordHash) {
		return Config{}, fmt.Errorf("invalid admin configuration hash or trailing data")
	}
	return cfg, nil
}

func WriteConfig(path, password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 16 || utf8.RuneCountInString(password) > 128 {
		return fmt.Errorf("admin password must contain 16–128 Unicode characters")
	}
	encoded, err := credential.Hash(password)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create admin configuration (refusing to overwrite): %w", err)
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if err := json.NewEncoder(f).Encode(Config{PasswordHash: encoded}); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
