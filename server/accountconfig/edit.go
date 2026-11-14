package accountconfig

import (
	"bytes"
	"context"
	"fmt"
	"reflect"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"gopkg.in/yaml.v3"
)

// CheckAdd validates the current file, proposed edit, and server overrides without writing.
func CheckAdd(ctx context.Context, target Target, account Account) error {
	target, err := target.resolve()
	if err != nil {
		return err
	}
	data, err := read(ctx, target.Path)
	if err != nil {
		return err
	}
	_, _, err = edit(data, target, account, "")
	return err
}

// Add appends an account using the current bytes under the config file lock.
// Its Change contains the effective account and exact bytes for Restore.
func Add(ctx context.Context, target Target, account Account) (Change, error) {
	return update(ctx, target, account, "")
}

// Remove deletes the account with id, leaving its credentials untouched.
// Its Change contains the removed effective account and exact bytes for Restore.
func Remove(ctx context.Context, target Target, id string) (Change, error) {
	if id == "" {
		return Change{}, ErrAccountNotFound
	}
	return update(ctx, target, Account{}, id)
}

func update(ctx context.Context, target Target, account Account, removeID string) (Change, error) {
	target, err := target.resolve()
	if err != nil {
		return Change{}, err
	}
	change := Change{Path: target.Path}
	err = credstore.UpdateFile(ctx, target.Path, 0o600, func(current []byte) ([]byte, error) {
		after, effective, err := edit(current, target, account, removeID)
		if err != nil {
			return nil, err
		}
		change.Before, change.After, change.Account = bytes.Clone(current), bytes.Clone(after), effective
		return after, nil
	})
	if err != nil {
		return Change{}, err
	}
	return change, nil
}

// Restore replaces written with previous only if the current bytes still equal written.
// A nil previous value restores an empty file, equivalent to a missing server config.
func Restore(ctx context.Context, target Target, previous, written []byte) error {
	target, err := target.resolve()
	if err != nil {
		return err
	}
	return credstore.UpdateFile(ctx, target.Path, 0o600, func(current []byte) ([]byte, error) {
		if !bytes.Equal(current, written) {
			return nil, ErrConfigChanged
		}
		return bytes.Clone(previous), nil
	})
}

func edit(data []byte, target Target, account Account, removeID string) ([]byte, Account, error) {
	if removeID == "" {
		expanded, err := ExpandConfigDir(account.ConfigDir)
		if err != nil {
			return nil, Account{}, err
		}
		account.ConfigDir = expanded
	}
	doc, sequence, keys, err := accountDocument(data)
	if err != nil {
		return nil, Account{}, layoutError(err, account, removeID)
	}
	original, err := check(data, target)
	if err != nil {
		return nil, Account{}, validationError(err)
	}
	expected := append([]Account{}, original...)
	index := -1
	var effective Account
	if removeID == "" {
		sequence.Content = append(sequence.Content, accountNode(account))
	} else {
		for i, a := range original {
			if a.ID == removeID {
				index, effective = i, a
				break
			}
		}
		if index < 0 {
			return nil, Account{}, ErrAccountNotFound
		}
		expected = append(expected[:index], expected[index+1:]...)
		sequence.Content = append(sequence.Content[:index], sequence.Content[index+1:]...)
	}
	after, err := encodeDocument(doc, data)
	if err != nil {
		return nil, Account{}, err
	}
	if err := verifyGeneric(data, after, keys, account, index); err != nil {
		return nil, Account{}, err
	}
	// The current file validated above, so a failure here is caused by the
	// requested account in the context of the whole file and its overrides.
	actual, err := check(after, target)
	if err != nil {
		return nil, Account{}, requestedAccountError(err)
	}
	if removeID == "" && len(actual) == len(original)+1 {
		effective = actual[len(original)]
		expected = append(expected, effective)
	}
	if !reflect.DeepEqual(actual, expected) {
		return nil, Account{}, ErrUnsupportedLayout
	}
	return after, effective, nil
}

func layoutError(err error, account Account, removeID string) error {
	if err != ErrUnsupportedLayout {
		return err
	}
	fields := accountFields(account)
	if removeID != "" {
		fields = map[string]any{"id": removeID}
	}
	snippet, encodeErr := yaml.Marshal(map[string]any{"providers": map[string]any{"claude": map[string]any{"accounts": []any{fields}}}})
	if encodeErr != nil {
		return err
	}
	return fmt.Errorf("%w; requested entry:\n%s", err, snippet)
}
