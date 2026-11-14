package safestorage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsDPAPIRoundTripAndDecrypt(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	in := windows.DataBlob{Size: uint32(len(key)), Data: &key[0]}
	var out windows.DataBlob
	err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))); err != nil {
			t.Error(err)
		}
	}()
	protected := bytes.Clone(unsafe.Slice(out.Data, out.Size))

	t.Run("unprotect", func(t *testing.T) {
		got, err := Unprotect(protected)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(got)
		if !bytes.Equal(got, key) {
			t.Fatal("unprotected key mismatch")
		}
	})

	t.Run("decrypt with platform unprotect", func(t *testing.T) {
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		nonce := bytes.Repeat([]byte{7}, 12)
		blob := gcm.Seal(append([]byte("v10"), nonce...), nonce, []byte("synthetic-cursor-token"), nil)
		state := filepath.Join(t.TempDir(), "Local State")
		encodedKey := base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), protected...))
		if err := os.WriteFile(state, []byte(`{"os_crypt":{"encrypted_key":"`+encodedKey+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := Decrypt("cursor-safe-storage:v1:"+base64.StdEncoding.EncodeToString(blob), state, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "synthetic-cursor-token" {
			t.Fatal("decrypted token mismatch")
		}
	})
}
