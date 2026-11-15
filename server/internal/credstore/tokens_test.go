package credstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type tokenDoc struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Marker  string `json:"marker,omitempty"`
}

func tokenCodec(warn func(error)) credstore.TokenCodec[tokenDoc] {
	return credstore.TokenCodec[tokenDoc]{
		Pair: func(file []byte) (credstore.TokenPair, error) {
			doc, err := decodeTokenDoc(file)
			if err != nil {
				return credstore.TokenPair{}, err
			}
			return credstore.TokenPair{Access: doc.Access, Refresh: doc.Refresh}, nil
		},
		Apply: func(file []byte, tokens tokenDoc) ([]byte, error) {
			doc, err := decodeTokenDoc(file)
			if err != nil {
				return nil, err
			}
			doc.Access, doc.Refresh = tokens.Access, tokens.Refresh
			return encodeTokenDoc(doc)
		},
		Of: func(tokens tokenDoc) credstore.TokenPair {
			return credstore.TokenPair{Access: tokens.Access, Refresh: tokens.Refresh}
		},
		Warn: warn,
	}
}

func decodeTokenDoc(data []byte) (tokenDoc, error) {
	var doc tokenDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return tokenDoc{}, err
	}
	return doc, nil
}

func encodeTokenDoc(doc tokenDoc) ([]byte, error) {
	return json.Marshal(doc)
}

func Test_RefreshTokens_saves_refreshed_tokens(t *testing.T) {
	// Given
	path := writeCreds(t, `{"access":"old","refresh":"old-r"}`, 0o600)
	var exchanges atomic.Int32

	// When
	result, outcome, err := credstore.RefreshTokens(context.Background(), path,
		credstore.TokenPair{Access: "old", Refresh: "old-r"}, tokenCodec(nil),
		func([]byte) (tokenDoc, error) {
			exchanges.Add(1)
			return tokenDoc{Access: "new", Refresh: "new-r"}, nil
		})

	// Then
	if err != nil {
		t.Fatalf("RefreshTokens: %v", err)
	}
	if outcome != credstore.Saved || result.Access != "new" || exchanges.Load() != 1 {
		t.Fatalf("outcome=%v result=%+v exchanges=%d", outcome, result, exchanges.Load())
	}
	got := string(readCredsBytes(t, path))
	if !strings.Contains(got, `"access":"new"`) || !strings.Contains(got, `"refresh":"new-r"`) {
		t.Fatalf("file = %s", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_RefreshTokens_adopts_changed_pair_without_exchange(t *testing.T) {
	// Given
	path := writeCreds(t, `{"access":"external","refresh":"external-r"}`, 0o600)
	var exchanges atomic.Int32

	// When
	result, outcome, err := credstore.RefreshTokens(context.Background(), path,
		credstore.TokenPair{Access: "old", Refresh: "old-r"}, tokenCodec(nil),
		func([]byte) (tokenDoc, error) {
			exchanges.Add(1)
			return tokenDoc{Access: "new", Refresh: "new-r"}, nil
		})

	// Then
	if err != nil {
		t.Fatalf("RefreshTokens: %v", err)
	}
	if outcome != credstore.Adopted || exchanges.Load() != 0 || result != (tokenDoc{}) {
		t.Fatalf("outcome=%v exchanges=%d result=%+v", outcome, exchanges.Load(), result)
	}
	if got := string(readCredsBytes(t, path)); !strings.Contains(got, `"access":"external"`) {
		t.Fatalf("file = %s", got)
	}
}

func Test_RefreshTokens_discards_result_superseded_during_exchange(t *testing.T) {
	// Given
	path := writeCreds(t, `{"access":"old","refresh":"old-r"}`, 0o600)

	// When
	result, outcome, err := credstore.RefreshTokens(context.Background(), path,
		credstore.TokenPair{Access: "old", Refresh: "old-r"}, tokenCodec(nil),
		func([]byte) (tokenDoc, error) {
			if err := os.WriteFile(path, []byte(`{"access":"external","refresh":"external-r"}`), 0o600); err != nil {
				return tokenDoc{}, err
			}
			return tokenDoc{Access: "new", Refresh: "new-r"}, nil
		})

	// Then
	if err != nil {
		t.Fatalf("RefreshTokens: %v", err)
	}
	if outcome != credstore.Adopted || result.Access != "new" {
		t.Fatalf("outcome=%v result=%+v", outcome, result)
	}
	if got := string(readCredsBytes(t, path)); !strings.Contains(got, `"access":"external"`) || strings.Contains(got, `"access":"new"`) {
		t.Fatalf("superseded result overwrote the file: %s", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_RefreshTokens_exchange_error_leaves_file_unchanged(t *testing.T) {
	// Given
	path := writeCreds(t, `{"access":"old","refresh":"old-r"}`, 0o600)
	wantErr := errors.New("exchange boom")

	// When
	_, outcome, err := credstore.RefreshTokens(context.Background(), path,
		credstore.TokenPair{Access: "old", Refresh: "old-r"}, tokenCodec(nil),
		func([]byte) (tokenDoc, error) {
			return tokenDoc{}, wantErr
		})

	// Then
	if !errors.Is(err, wantErr) || outcome != credstore.Adopted {
		t.Fatalf("err=%v outcome=%v", err, outcome)
	}
	if got := string(readCredsBytes(t, path)); got != `{"access":"old","refresh":"old-r"}` {
		t.Fatalf("file = %s", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func Test_RefreshTokens_rejects_incomplete_codec(t *testing.T) {
	path := writeCreds(t, `{"access":"old","refresh":"old-r"}`, 0o600)
	_, _, err := credstore.RefreshTokens(context.Background(), path, credstore.TokenPair{},
		credstore.TokenCodec[tokenDoc]{}, func([]byte) (tokenDoc, error) { return tokenDoc{}, nil })
	if !errors.Is(err, credstore.ErrTokenCodec) {
		t.Fatalf("err = %v, want ErrTokenCodec", err)
	}
}

func readCredsBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	return data
}
