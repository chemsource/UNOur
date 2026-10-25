package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRejectsInsecureFilesAndHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	if err := WriteConfig(path, "a long admin password"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.PasswordHash == "" {
		t.Fatalf("load: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "a long admin password") {
		t.Fatal("plaintext password stored")
	}
	if err := WriteConfig(path, "another long password"); err == nil {
		t.Fatal("overwrote existing config")
	}
	for _, mode := range []os.FileMode{0644, 0640, 0700, 0200} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("unsafe configuration mode %o accepted", mode)
		}
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("private read-only configuration rejected: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(link); err == nil {
		t.Fatal("symlink accepted")
	}
	for _, body := range []string{`{"password":"secret"}`, `{"password_hash":"sha256$weak"}`, `{"password_hash":"` + cfg.PasswordHash + `","unknown":true}`, string(raw) + `{}`, strings.Repeat("x", 9000)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if err := WriteConfig(filepath.Join(t.TempDir(), "weak.json"), "short"); err == nil {
		t.Fatal("weak admin password accepted")
	}
}
