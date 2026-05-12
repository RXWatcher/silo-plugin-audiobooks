package server

import "crypto/rand"

// randomBytes generates n cryptographically-random bytes. Used to mint new
// JWT secrets when the admin rotates them.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
