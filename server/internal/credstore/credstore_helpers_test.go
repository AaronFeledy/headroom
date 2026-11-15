package credstore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ownedDoc struct {
	Fields map[string]string `json:"fields,omitempty"`
}

func decodeOwnedDoc(data []byte) (ownedDoc, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ownedDoc{}, err
	}
	var doc ownedDoc
	if raw, ok := envelope["fields"]; ok {
		if err := json.Unmarshal(raw, &doc.Fields); err != nil {
			return ownedDoc{}, err
		}
	}
	if doc.Fields == nil {
		doc.Fields = map[string]string{}
	}
	return doc, nil
}

func encodePreservingUnknown(data []byte, doc ownedDoc) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	fields, err := json.Marshal(doc.Fields)
	if err != nil {
		return nil, err
	}
	envelope["fields"] = fields
	return json.Marshal(envelope)
}

func writeCreds(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return path
}

func assertFileBytesAndMode(t *testing.T, path string, wantBytes []byte, wantMode os.FileMode) {
	t.Helper()
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("bytes = %s, want %s", gotBytes, wantBytes)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if !modeMatches(info.Mode(), wantMode) {
		t.Fatalf("mode = %v, want %v", info.Mode().Perm(), wantMode.Perm())
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temp artifact remains: %s", entry.Name())
		}
	}
}
