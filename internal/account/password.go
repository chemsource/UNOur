package account

import "unour/internal/credential"

const passwordIterations = credential.Iterations

func hashPassword(password string) (string, error) { return credential.Hash(password) }
func verifyPassword(encoded, password string) bool { return credential.Verify(encoded, password) }
