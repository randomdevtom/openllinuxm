package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A command that leaves a process running in the background must not hang the tool.
func TestRunBackgroundDoesNotHang(t *testing.T) {
	start := time.Now()
	out, ok := Run(context.Background(), t.TempDir(), "run", map[string]any{"command": "sleep 30 & echo started"})
	if !ok || !strings.Contains(out, "started") {
		t.Fatalf("got %q ok=%v", out, ok)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

// Cancelling stops the whole command, including what it started.
func TestRunCancelKillsGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	Run(ctx, t.TempDir(), "run", map[string]any{"command": "sleep 30 | cat"})
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestRunOutputAndExitCode(t *testing.T) {
	out, ok := Run(context.Background(), t.TempDir(), "run", map[string]any{"command": "echo hi; echo err >&2; exit 3"})
	if ok || !strings.Contains(out, "exit 3") || !strings.Contains(out, "hi") || !strings.Contains(out, "err") {
		t.Fatalf("got %q ok=%v", out, ok)
	}
}
