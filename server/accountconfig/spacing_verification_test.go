package accountconfig

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"
)

func Test_encodeVerified_falls_back_when_spacing_changes_scalar_value(t *testing.T) {
	original := []byte("key: |\n  first\n\n  second\n")
	var doc yaml.Node
	if err := yaml.Unmarshal(original, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Content[0].Content[1].Value = "first\nsecond\n"
	result, err := encodeVerified(&doc, original, func(candidate []byte) error {
		var value map[string]string
		if err := yaml.Unmarshal(candidate, &value); err != nil {
			return err
		}
		if value["key"] != "first\nsecond\n" {
			return ErrUnsupportedLayout
		}
		return nil
	})
	if err != nil || !bytes.Equal(result, []byte("key: |\n  first\n  second\n")) {
		t.Fatalf("result=%q error=%v", result, err)
	}
}
