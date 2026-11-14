package accountconfig

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

func accountDocument(data []byte) (*yaml.Node, *yaml.Node, []string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	var doc yaml.Node
	err := decoder.Decode(&doc)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, nil, &ValidationError{cause: err}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, nil, ErrUnsupportedLayout
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" && root.Value == "" {
		root.Kind, root.Tag = yaml.MappingNode, "!!map"
	}
	if err := editableMapping(root); err != nil {
		return nil, nil, nil, err
	}
	providers, _, err := mappingChild(root, "providers", false)
	if err != nil {
		return nil, nil, nil, err
	}
	claude, key, err := mappingChild(providers, "claude", true)
	if err != nil {
		return nil, nil, nil, err
	}
	var accounts *yaml.Node
	for i := 0; i < len(claude.Content); i += 2 {
		if claude.Content[i].Value == "accounts" {
			accounts = claude.Content[i+1]
			break
		}
	}
	if accounts == nil {
		accounts = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		claude.Content = append(claude.Content, strNode("accounts"), accounts)
	}
	if err := editableTree(accounts); err != nil {
		return nil, nil, nil, err
	}
	if accounts.Tag == "!!null" {
		accounts.Kind, accounts.Tag, accounts.Value, accounts.Style = yaml.SequenceNode, "!!seq", "", 0
	}
	if accounts.Kind != yaml.SequenceNode {
		return nil, nil, nil, ErrUnsupportedLayout
	}
	accounts.Style = 0
	return &doc, accounts, []string{"providers", key, "accounts"}, nil
}

func editableMapping(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || node.Anchor != "" {
		return ErrUnsupportedLayout
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || key.Value == "<<" {
			return ErrUnsupportedLayout
		}
	}
	return nil
}

func mappingChild(parent *yaml.Node, name string, folded bool) (*yaml.Node, string, error) {
	var found *yaml.Node
	key := name
	for i := 0; i < len(parent.Content); i += 2 {
		value := parent.Content[i].Value
		if value == name || folded && strings.ToLower(value) == name {
			if found != nil {
				return nil, "", ErrUnsupportedLayout
			}
			found, key = parent.Content[i+1], value
		}
	}
	if found == nil {
		found = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		parent.Content = append(parent.Content, strNode(name), found)
	}
	if err := editableMapping(found); err != nil {
		return nil, "", err
	}
	return found, key, nil
}

func editableTree(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return ErrUnsupportedLayout
	}
	for _, child := range node.Content {
		if err := editableTree(child); err != nil {
			return err
		}
	}
	return nil
}

func strNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func accountNode(account Account) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{strNode("id"), strNode(account.ID)}}
	if account.Label != "" {
		node.Content = append(node.Content, strNode("label"), strNode(account.Label))
	}
	node.Content = append(node.Content, strNode("config_dir"), strNode(account.ConfigDir))
	if !account.Enabled {
		node.Content = append(node.Content, strNode("enabled"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
	}
	return node
}

func accountFields(account Account) map[string]any {
	fields := map[string]any{"id": account.ID, "config_dir": account.ConfigDir}
	if account.Label != "" {
		fields["label"] = account.Label
	}
	if !account.Enabled {
		fields["enabled"] = false
	}
	return fields
}

func encodeDocument(doc *yaml.Node, original []byte) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(inferIndent(original))
	if err := encoder.Encode(doc); err != nil {
		return nil, &ValidationError{cause: err}
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	data := buffer.Bytes()
	crlf := bytes.Contains(original, []byte("\r\n"))
	if crlf {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}
	// yaml.v3 drops a document that holds only comments, so keep that text
	// verbatim ahead of the new content.
	if commentOnly(original) {
		prefix := bytes.Clone(original)
		if !bytes.HasSuffix(prefix, []byte("\n")) {
			if crlf {
				prefix = append(prefix, '\r')
			}
			prefix = append(prefix, '\n')
		}
		return append(prefix, data...), nil
	}
	if bytes.HasPrefix(original, []byte{0xef, 0xbb, 0xbf}) {
		data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	}
	return data, nil
}

func commentOnly(data []byte) bool {
	found := false
	for _, line := range strings.Split(string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			return false
		}
		found = true
	}
	return found
}

func inferIndent(data []byte) int {
	indent := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		n := len(line) - len(trimmed)
		if n == 0 || strings.TrimSpace(trimmed) == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indent == 0 {
			indent = n
		} else {
			for n != 0 {
				indent, n = n, indent%n
			}
		}
	}
	if indent == 0 {
		return 2
	}
	return indent
}
