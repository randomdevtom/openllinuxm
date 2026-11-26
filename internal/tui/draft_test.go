package tui

import "testing"

func TestDraftLabel(t *testing.T) {
	cases := map[string]string{
		"<tool_call>\n<function=write_file>\n<parameter=path>\na.html\n</parameter>\n<parameter=content>\n1\n2\n3\n": "writing " + toolTitle("write_file") + " · 9 lines",
		"<tool_call>\n<function=wri":    "writing a tool call",
		`{"path":"a.txt","content":"x"}`: "writing a tool call",
	}
	for in, want := range cases {
		if got := draftLabel(in); got != want {
			t.Errorf("draftLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
