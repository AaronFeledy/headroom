package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadinessMarkerCorpus(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "readiness.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Nonce   string `json:"nonce"`
		Version string `json:"version"`
		Cases   []struct {
			Name   string          `json:"name"`
			Marker json.RawMessage `json:"marker"`
			Raw    string          `json:"raw"`
			Accept bool            `json:"accept"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "generation with spaces", "headroom")
	encodedPath, err := json.Marshal(executable)
	if err != nil {
		t.Fatal(err)
	}
	expected := readinessMarker{Nonce: corpus.Nonce, Version: corpus.Version, PID: 42, Executable: executable}
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			raw := test.Raw
			if raw == "" {
				raw = string(test.Marker)
			}
			raw = strings.ReplaceAll(raw, "@EXE_JSON@", string(encodedPath))
			raw = strings.ReplaceAll(raw, "@EXE@", string(encodedPath[1:len(encodedPath)-1]))
			if got := matchesReadinessMarker([]byte(raw), expected); got != test.Accept {
				t.Fatalf("accepted = %v, want %v", got, test.Accept)
			}
		})
	}
	for _, test := range []struct {
		name   string
		marker readinessMarker
		accept bool
	}{
		{"exact child PID", readinessMarker{corpus.Nonce, 43, corpus.Version, executable}, false},
		{"clean path", readinessMarker{corpus.Nonce, 42, corpus.Version, filepath.Dir(executable) + string(os.PathSeparator) + "." + string(os.PathSeparator) + "headroom"}, true},
		{"path case", readinessMarker{corpus.Nonce, 42, corpus.Version, strings.ToUpper(executable)}, runtimeWindows()},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.marker)
			if err != nil {
				t.Fatal(err)
			}
			if got := matchesReadinessMarker(raw, expected); got != test.accept {
				t.Fatalf("accepted = %v, want %v", got, test.accept)
			}
		})
	}
	t.Run("bounded marker", func(t *testing.T) {
		raw, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		if matchesReadinessMarker(append(raw, []byte(strings.Repeat(" ", 4096))...), expected) {
			t.Fatal("oversized marker accepted")
		}
	})
}
