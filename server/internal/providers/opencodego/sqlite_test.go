package opencodego

import "testing"

func Test_readOnlyDSN(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "unix path", path: "/home/x/opencode.db", want: "file:///home/x/opencode.db?mode=ro"},
		{name: "windows drive path", path: "C:/Users/x/opencode.db", want: "file:///C:/Users/x/opencode.db?mode=ro"},
		{name: "path with space", path: "/home/x/opencode db/opencode.db", want: "file:///home/x/opencode%20db/opencode.db?mode=ro"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given the slash-form path
			// When the read-only DSN is built
			got := readOnlyDSN(test.path)

			// Then the SQLite file URI keeps its leading slash and encodes the path
			if got != test.want {
				t.Fatalf("readOnlyDSN(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
