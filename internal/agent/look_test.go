package agent

import "testing"

func TestUnaskedLook(t *testing.T) {
	cases := []struct {
		ask, tool string
		args      map[string]any
		want      bool
	}{
		{"hey how are you", "list_files", map[string]any{"path": "."}, true},
		{"what's the capital of france", "run", map[string]any{"command": "ls -la"}, true},
		{"tell me a joke", "read_file", map[string]any{"path": "README.md"}, true},
		{"tell me a joke", "read_file", map[string]any{"path": "AGENTS.md"}, true},
		{"what time is it", "run", map[string]any{"command": "date"}, false},
		{"fix the bug in main.go", "list_files", map[string]any{"path": "."}, false},
		{"what's in this folder", "list_files", map[string]any{}, false},
		{"read the readme", "read_file", map[string]any{"path": "README.md"}, false},
		{"set up a python project", "run", map[string]any{"command": "ls"}, false},
		{"continue\nbuild the app in ~/game/", "list_files", map[string]any{}, false},
	}
	for _, c := range cases {
		if got := unaskedLook(c.ask, c.tool, c.args); got != c.want {
			t.Errorf("unaskedLook(%q, %s %v) = %v, want %v", c.ask, c.tool, c.args, got, c.want)
		}
	}
}
