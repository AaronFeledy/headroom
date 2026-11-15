package credstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func Test_Refresh_preserves_unknown_fields_and_valid_json_when_concurrent(t *testing.T) {
	// Given
	path := writeCreds(t, `{"external_owner":{"sentinel":"keep","version":1}}`, 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const workers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	readResultCh := make(chan readResult, 1)
	writersDone := make(chan struct{})
	readerReady := make(chan struct{})
	go func() {
		close(readerReady)
		readResultCh <- readContinuouslyUntilDone(path, writersDone)
	}()
	<-readerReady

	// When
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("field_%02d", i)
			errCh <- credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
				return func(latest []byte) ([]byte, error) {
					doc, err := decodeOwnedDoc(latest)
					if err != nil {
						return nil, err
					}
					doc.Fields[name] = "updated"
					return encodePreservingUnknown(latest, doc)
				}, nil
			})
		}(i)
	}
	wg.Wait()
	close(writersDone)
	close(errCh)

	// Then
	readResult := <-readResultCh
	if readResult.err != nil {
		t.Fatalf("continuous reader saw invalid file: %v", readResult.err)
	}
	if readResult.observations == 0 {
		t.Fatal("continuous reader observed no file versions")
	}
	for err := range errCh {
		if err != nil {
			t.Fatalf("Refresh returned error: %v", err)
		}
	}
	finalBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	assertExternalOwnerPreserved(t, finalBytes)
	final, err := decodeOwnedDoc(finalBytes)
	if err != nil {
		t.Fatalf("decode final owned fields: %v", err)
	}
	for i := range workers {
		name := fmt.Sprintf("field_%02d", i)
		if final.Fields[name] != "updated" {
			t.Fatalf("%s = %q, want updated", name, final.Fields[name])
		}
	}
}

type readResult struct {
	observations int
	err          error
}

func readContinuouslyUntilDone(path string, done <-chan struct{}) readResult {
	observations := 0
	for {
		select {
		case <-done:
			return readResult{observations: observations}
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return readResult{observations: observations, err: err}
		}
		if err := validateObservableVersion(data); err != nil {
			return readResult{observations: observations, err: err}
		}
		observations++
	}
}

func validateObservableVersion(data []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if string(envelope["external_owner"]) != `{"sentinel":"keep","version":1}` {
		return fmt.Errorf("external_owner = %s", envelope["external_owner"])
	}
	if raw, ok := envelope["fields"]; ok {
		var fields map[string]string
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for key, value := range fields {
			if value != "updated" {
				return fmt.Errorf("%s = %q", key, value)
			}
		}
	}
	return nil
}

func assertExternalOwnerPreserved(t *testing.T, data []byte) {
	t.Helper()
	if err := validateObservableVersion(data); err != nil {
		t.Fatalf("external owner not preserved: %v", err)
	}
}
