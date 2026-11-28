package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/randomdevtom/openllinuxm/internal/llm"
)

func TestCompact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"SUMMARY: chapters 1-3 done, next chapter 4"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	a := New(t.TempDir())
	a.Srv = &llm.Server{URL: srv.URL, Kind: "openai"}
	a.Send = func(any) {}
	a.msgs = append(a.msgs, llm.Message{Role: "user", Content: "[working directory: /x]\ntranslate the book"})
	for i := 0; i < 10; i++ {
		id := fmt.Sprint("c", i)
		a.msgs = append(a.msgs,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Type: "function"}}},
			llm.Message{Role: "tool", Content: strings.Repeat("x", 5000), ToolCallID: id})
	}
	last := a.msgs[len(a.msgs)-1].ToolCallID
	n, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || len(a.msgs) > keepTail+3 {
		t.Fatalf("folded %d, %d messages left", n, len(a.msgs))
	}
	if a.msgs[0].Role != "system" || !strings.Contains(a.msgs[1].Content, "SUMMARY: chapters 1-3 done") {
		t.Fatalf("summary not at the start: %+v", a.msgs[1])
	}
	if a.msgs[2].Role == "tool" {
		t.Fatal("tail starts with an orphaned tool result")
	}
	if a.msgs[len(a.msgs)-1].ToolCallID != last {
		t.Fatal("latest message lost")
	}
}

func TestTooLong(t *testing.T) {
	if !tooLong(nil, &llm.ChatResult{Content: "[prompt too long]"}) || tooLong(nil, &llm.ChatResult{Content: "fine"}) {
		t.Fatal("tooLong")
	}
}
