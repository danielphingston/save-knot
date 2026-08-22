package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func NewID(now time.Time) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate random ID: %w", err)
	}
	return fmt.Sprintf("%013x%s", now.UnixMilli(), hex.EncodeToString(random[:])), nil
}
