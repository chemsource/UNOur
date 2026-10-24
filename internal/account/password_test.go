package account

import (
	"strings"
	"testing"
)

func TestPasswordLengthIsEightToFourteenCharacters(t *testing.T) {
	for _, tc := range []struct {
		password string
		want     bool
	}{
		{"1234567", false}, {"12345678", true}, {"12345678901234", true}, {"123456789012345", false},
		{strings.Repeat("😀", 8), true}, {strings.Repeat("😀", 14), true}, {strings.Repeat("😀", 15), false},
	} {
		if got := validPassword(tc.password); got != tc.want {
			t.Errorf("password length %d: got %v want %v", len([]rune(tc.password)), got, tc.want)
		}
	}
}

func TestPasswordSaltsAndVerification(t *testing.T) {
	a, err := hashPassword("valid-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword("valid-password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || strings.Contains(a, "valid-password") {
		t.Fatal("passwords need independent salts and must not be stored as plaintext")
	}
	if !verifyPassword(a, "valid-password") || verifyPassword(a, "wrong-password") || verifyPassword("corrupt", "valid-password") {
		t.Fatal("password verification failed")
	}
}
