package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Quarry's tool_call_text deltas reach the callback as progress, and the parsed call still arrives.
func TestChatToolProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range []string{
			`{"choices":[{"delta":{"content":"Writing."}}]}`,
			`{"choices":[{"delta":{"tool_call_text":"<tool_call>\n<function=write_file>\n"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", d)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	s := &Server{URL: srv.URL}
	var tool, text string
	res, err := s.Chat(context.Background(), "m", []Message{{Role: "user", Content: "x"}}, nil, false, func(e StreamEvent) { tool += e.Tool; text += e.Text })
	if err != nil {
		t.Fatal(err)
	}
	if text != "Writing." || tool != "<tool_call>\n<function=write_file>\n"+`{"path":"a"}` {
		t.Fatalf("text %q tool %q", text, tool)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("calls %+v", res.ToolCalls)
	}
}
