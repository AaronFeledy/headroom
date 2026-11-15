package accountconfig

import (
	"strings"

	"gopkg.in/yaml.v3"
)

func encodeVerified(doc *yaml.Node, original []byte, verify func([]byte) error) ([]byte, error) {
	plain, err := encodeDocument(doc, original)
	if err != nil {
		return nil, err
	}
	spaced := preserveBlankLines(original, plain)
	if err := verify(spaced); err == nil {
		return spaced, nil
	}
	if err := verify(plain); err != nil {
		return nil, err
	}
	return plain, nil
}

type contentLine struct {
	text, normalized string
	blanks           []string
}

func contentLines(data []byte) []contentLine {
	var lines []contentLine
	var blanks []string
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue
		}
		if strings.TrimSpace(line) == "" {
			blanks = append(blanks, line)
			continue
		}
		lines = append(lines, contentLine{text: line, normalized: strings.Join(strings.Fields(line), " "), blanks: blanks})
		blanks = nil
	}
	// A terminal sentinel also preserves trailing blank lines.
	return append(lines, contentLine{blanks: blanks})
}

// Match nonblank lines by LCS. Preserve the larger blank run at a match:
// yaml.v3 already retains blank lines inside literal scalar values.
func preserveBlankLines(original, encoded []byte) []byte {
	a, b := contentLines(original), contentLines(encoded)
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i].normalized == b[j].normalized {
				lcs[i][j] = 1 + lcs[i+1][j+1]
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i].normalized == b[j].normalized:
			if len(a[i].blanks) > len(b[j].blanks) {
				b[j].blanks = a[i].blanks
			}
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	var output strings.Builder
	for _, line := range b {
		for _, blank := range line.blanks {
			output.WriteString(blank)
		}
		output.WriteString(line.text)
	}
	return []byte(output.String())
}
