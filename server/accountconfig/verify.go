package accountconfig

import (
	"reflect"

	"gopkg.in/yaml.v3"
)

func verifyGeneric(before, after []byte, keys []string, account Account, index int, changes *AccountChanges) error {
	var original, candidate any
	if err := yaml.Unmarshal(before, &original); err != nil {
		return &ValidationError{cause: err}
	}
	if err := yaml.Unmarshal(after, &candidate); err != nil {
		return &ValidationError{cause: err}
	}
	if original == nil {
		original = map[string]any{}
	}
	parent := original
	for _, key := range keys[:2] {
		value, ok := genericGet(parent, key)
		if !ok {
			value = map[string]any{}
			if !genericSet(parent, key, value) {
				return ErrUnsupportedLayout
			}
		}
		parent = value
	}
	value, _ := genericGet(parent, "accounts")
	var items []any
	if value != nil {
		var ok bool
		items, ok = value.([]any)
		if !ok {
			return ErrUnsupportedLayout
		}
	}
	if changes != nil {
		item := items[index]
		if changes.Label != nil && !genericSet(item, "label", *changes.Label) {
			return ErrUnsupportedLayout
		}
		if changes.Enabled != nil {
			if *changes.Enabled {
				switch mapping := item.(type) {
				case map[string]any:
					delete(mapping, "enabled")
				case map[any]any:
					delete(mapping, "enabled")
				default:
					return ErrUnsupportedLayout
				}
			} else if !genericSet(item, "enabled", false) {
				return ErrUnsupportedLayout
			}
		}
	} else if index < 0 {
		items = append(items, accountFields(account))
	} else {
		items = append(items[:index], items[index+1:]...)
	}
	if len(items) == 0 {
		items = []any{}
	}
	if !genericSet(parent, "accounts", items) || !reflect.DeepEqual(original, candidate) {
		return ErrUnsupportedLayout
	}
	return nil
}

func genericGet(value any, key string) (any, bool) {
	switch mapping := value.(type) {
	case map[string]any:
		item, ok := mapping[key]
		return item, ok
	case map[any]any:
		item, ok := mapping[key]
		return item, ok
	default:
		return nil, false
	}
}

func genericSet(value any, key string, item any) bool {
	switch mapping := value.(type) {
	case map[string]any:
		mapping[key] = item
	case map[any]any:
		mapping[key] = item
	default:
		return false
	}
	return true
}
