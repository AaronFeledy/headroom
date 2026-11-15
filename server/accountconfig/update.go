package accountconfig

import (
	"bytes"
	"context"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
	"gopkg.in/yaml.v3"
)

// Update changes only the matching account's requested label and enabled fields.
// It validates current bytes under the file lock and returns the effective account
// and exact before/after bytes for Restore. Enabling removes the enabled key.
func Update(ctx context.Context, target Target, id string, changes AccountChanges) (Change, error) {
	if changes.Label == nil && changes.Enabled == nil {
		return Change{}, ErrNoChanges
	}
	target, err := target.resolve()
	if err != nil {
		return Change{}, err
	}
	change := Change{Path: target.Path}
	err = credstore.UpdateFile(ctx, target.Path, 0o600, func(current []byte) ([]byte, error) {
		doc, sequence, keys, err := accountDocument(current)
		if err != nil {
			return nil, err
		}
		original, err := check(current, target)
		if err != nil {
			return nil, validationError(err)
		}
		index := -1
		for i, account := range original {
			if account.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, ErrAccountNotFound
		}
		item := sequence.Content[index]
		if changes.Label != nil {
			setAccountScalar(item, "label", strNode(*changes.Label))
		}
		if changes.Enabled != nil {
			var value *yaml.Node
			if !*changes.Enabled {
				value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
			}
			setAccountScalar(item, "enabled", value)
		}
		var effective Account
		after, err := encodeVerified(doc, current, func(candidate []byte) error {
			if err := verifyGeneric(current, candidate, keys, Account{}, index, &changes); err != nil {
				return err
			}
			accounts, err := check(candidate, target)
			if err != nil {
				return requestedAccountError(err)
			}
			if len(accounts) != len(original) {
				return ErrUnsupportedLayout
			}
			for i, account := range accounts {
				if i != index && account != original[i] {
					return ErrUnsupportedLayout
				}
			}
			effective = accounts[index]
			return nil
		})
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

func setAccountScalar(item *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(item.Content); i += 2 {
		if item.Content[i].Value != key {
			continue
		}
		if value == nil {
			item.Content = append(item.Content[:i], item.Content[i+2:]...)
		} else {
			old := item.Content[i+1]
			old.Tag, old.Value = value.Tag, value.Value
		}
		return
	}
	if value != nil {
		item.Content = append(item.Content, strNode(key), value)
	}
}
