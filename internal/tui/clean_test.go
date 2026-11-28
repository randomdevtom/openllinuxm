package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestClean(t *testing.T) {
	cases := map[string]string{
		"\x1b[31mred\x1b[0m text":          "red text",
		"downloading 10%\r50%\r100%\ndone": "100%\ndone",
		"a\r\nb":                           "a\nb",
		"tab\there":                        "tab\there",
		"bell\a back\bspace":               "bell backspace",
		"\x1b]0;title\x07after":            "after",
		"warn ⚠️ ok":                       "warn ⚠ ok",
		"fam 👨‍👩‍👧":                        "fam 👨👩👧",
		"plain":                            "plain",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateWidth(t *testing.T) {
	s := truncate("日本語のテキストがとても長い行です", 10)
	if w := ansi.StringWidth(s); w > 10 {
		t.Errorf("truncate gave %d columns, want <= 10: %q", w, s)
	}
}
