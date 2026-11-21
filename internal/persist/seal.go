// Package persist writes HMAC-sealed state to disk: spent tokens and
// withdrawal intents. Files live in a 0700 directory, mode 0600; they
// contain no secrets. The MAC uses the proxy's secret_key.
package persist

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrTampered is returned when a record's HMAC does not match.
var ErrTampered = errors.New("tampered or unreadable record")

// ErrCorrupt means the state file could not be trusted (wrong secret,
// or every record was tampered). Callers must fail safe, not start empty.
var ErrCorrupt = errors.New("state could not be verified")

// Seal returns one line: hex(HMAC-SHA256(key, payload)) + space + payload.
func Seal(key, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)) + " " + string(payload)
}

// Unseal checks the MAC and returns the payload of one sealed line.
func Unseal(key []byte, line string) ([]byte, error) {
	line = strings.TrimSpace(line)
	macHex, payload, ok := strings.Cut(line, " ")
	if !ok || macHex == "" || payload == "" {
		return nil, ErrTampered
	}
	want, err := hex.DecodeString(macHex)
	if err != nil || len(want) != sha256.Size {
		return nil, ErrTampered
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	if !hmac.Equal(want, mac.Sum(nil)) {
		return nil, ErrTampered
	}
	return []byte(payload), nil
}
