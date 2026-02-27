package ws

import (
	"crypto/sha256"
	"fmt"
)

func hashToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return fmt.Sprintf("%x", h)
}
