// Package safestorage reads Cursor app tokens protected by Windows safeStorage.
package safestorage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

var (
	ErrUnsupported = errors.New("safestorage: unsupported encryption")
	ErrInvalid     = errors.New("safestorage: invalid encrypted data")
	ErrUnprotect   = errors.New("safestorage: cannot unprotect key")
)

// UnprotectFunc decrypts a key using the current user's OS protection context.
type UnprotectFunc func([]byte) ([]byte, error)

func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, "cursor-safe-storage:")
}

// Decrypt authenticates a v1 token using its own Cursor root's Local State.
// A nil unprotect selects the platform implementation.
func Decrypt(value, localStatePath string, unprotect UnprotectFunc) (string, error) {
	encoded, ok := strings.CutPrefix(value, "cursor-safe-storage:v1:")
	if !ok {
		return "", ErrUnsupported
	}
	blob, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(blob) < 3+12+16 {
		return "", ErrInvalid
	}
	if string(blob[:3]) != "v10" {
		return "", ErrUnsupported
	}
	data, err := os.ReadFile(localStatePath)
	if err != nil {
		return "", ErrInvalid
	}
	var state struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", ErrInvalid
	}
	protected, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil {
		return "", ErrInvalid
	}
	protected, ok = bytes.CutPrefix(protected, []byte("DPAPI"))
	if !ok {
		return "", ErrUnsupported
	}
	if len(protected) == 0 {
		return "", ErrInvalid
	}
	if unprotect == nil {
		unprotect = Unprotect
	}
	key, err := unprotect(protected)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return "", ErrUnsupported
		}
		return "", ErrUnprotect
	}
	defer clear(key)
	if len(key) != 32 {
		return "", ErrInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", ErrInvalid
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", ErrInvalid
	}
	plaintext, err := gcm.Open(nil, blob[3:15], blob[15:], nil)
	if err != nil {
		return "", ErrInvalid
	}
	defer clear(plaintext)
	return string(plaintext), nil
}
