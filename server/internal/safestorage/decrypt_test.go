package safestorage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDecryptAuthenticatesCursorToken(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{7}, 12)
	blob := append([]byte("v10"), nonce...)
	blob = gcm.Seal(blob, nonce, []byte("fixture-token"), nil)
	encode := func(b []byte) string { return "cursor-safe-storage:v1:" + base64.StdEncoding.EncodeToString(b) }
	tampered := bytes.Clone(blob)
	tampered[len(tampered)-1] ^= 1
	unknown := bytes.Clone(blob)
	copy(unknown, "v20")
	state := filepath.Join(t.TempDir(), "Local State")
	if err := os.WriteFile(state, []byte(`{"os_crypt":{"encrypted_key":"`+base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), key...))+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, value, path string
		unprotect         UnprotectFunc
		wantErr           error
	}{
		{"valid", encode(blob), state, bytesIdentity, nil},
		{"tampered tag", encode(tampered), state, bytesIdentity, ErrInvalid},
		{"short blob", encode(blob[:30]), state, bytesIdentity, ErrInvalid},
		{"bad base64", "cursor-safe-storage:v1:!", state, bytesIdentity, ErrInvalid},
		{"unknown version", "cursor-safe-storage:v2:AAAA", state, bytesIdentity, ErrUnsupported},
		{"unknown blob", encode(unknown), state, bytesIdentity, ErrUnsupported},
		{"plaintext", "fixture-token", state, bytesIdentity, ErrUnsupported},
		{"short key", encode(blob), state, func([]byte) ([]byte, error) { return make([]byte, 16), nil }, ErrInvalid},
		{"missing state", encode(blob), state + "-missing", bytesIdentity, ErrInvalid},
		{"unprotect failure", encode(blob), state, func([]byte) ([]byte, error) { return nil, errors.New("secret-fixture") }, ErrUnprotect},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decrypt(tt.value, tt.path, tt.unprotect)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr == nil && got != "fixture-token" {
				t.Fatal("plaintext mismatch")
			}
			if tt.wantErr != nil && got != "" {
				t.Fatal("failed decryption returned plaintext")
			}
		})
	}
}

func bytesIdentity(value []byte) ([]byte, error) { return bytes.Clone(value), nil }

func TestIsEncryptedRecognizesOnlyCursorPrefix(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{
		{"cursor-safe-storage:v1:AAAA", true}, {"cursor-safe-storage:v2:AAAA", true},
		{"cursor-safe-storage:", true}, {"cursor-safe-storage", false},
		{"xcursor-safe-storage:v1:AAAA", false}, {"fixture-token", false}, {"", false},
	} {
		if IsEncrypted(tt.value) != tt.want {
			t.Fatal("prefix classification mismatch")
		}
	}
}

func TestDecryptRejectsInvalidLocalState(t *testing.T) {
	value := "cursor-safe-storage:v1:" + base64.StdEncoding.EncodeToString(append([]byte("v10"), make([]byte, 28)...))
	for _, data := range []string{"{", `{}`, `{"os_crypt":{"encrypted_key":"!"}}`, `{"os_crypt":{"encrypted_key":"RFBBUEk="}}`, `{"os_crypt":{"encrypted_key":"b3RoZXI="}}`} {
		path := filepath.Join(t.TempDir(), "Local State")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Decrypt(value, path, func([]byte) ([]byte, error) { t.Fatal("invalid state reached unprotect"); return nil, nil })
		if err == nil {
			t.Fatal("invalid state accepted")
		}
	}
}
