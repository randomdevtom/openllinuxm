package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImportLogAndLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	log := filepath.Join(t.TempDir(), "log.txt")
	os.WriteFile(log, []byte(`[2026-11-26T16:00:00Z] user: old session
[2026-11-26T17:02:00Z] user: [working directory: /tmp]
make a file
[2026-11-26T17:02:05Z] assistant: Writing it.
[2026-11-26T17:02:06Z] tool: write_file {"path":"a.txt","content":"x -> y"} -> wrote a.txt
second line
[2026-11-26T17:02:07Z] assistant: Done.
`), 0o600)
	since, _ := time.Parse(time.RFC3339, "2026-11-26T17:01:41Z")
	path, n, err := ImportLog(log, since)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("got %d messages", n)
	}
	a := New(t.TempDir())
	if err := a.Load(path); err != nil {
		t.Fatal(err)
	}
	m := a.Messages()
	if m[0].Role != "system" || m[1].Content != "[working directory: /tmp]\nmake a file" {
		t.Fatalf("bad start: %+v", m[:2])
	}
	if len(m[2].ToolCalls) != 1 || m[2].ToolCalls[0].Function.Arguments != `{"path":"a.txt","content":"x -> y"}` {
		t.Fatalf("bad call: %+v", m[2])
	}
	if m[3].Role != "tool" || m[3].Content != "wrote a.txt\nsecond line" || m[3].ToolCallID != m[2].ToolCalls[0].ID {
		t.Fatalf("bad result: %+v", m[3])
	}
	if a.Dir != "/tmp" {
		t.Fatalf("dir %q", a.Dir)
	}
	// saving again goes to the same file
	a.msgs = append(a.msgs, m[1])
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	if p, _ := LatestSession(); p != path {
		t.Fatalf("latest %q want %q", p, path)
	}
}
