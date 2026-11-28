package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/randomdevtom/openllinuxm/internal/llm"
)

// Compaction: when the conversation gets close to the model's context size, the older part is
// replaced by a summary the model writes itself, and the work goes on from there. Long jobs
// (translating a book, big refactors) would otherwise hit the context limit and stop.

const (
	compactAt   = 0.75 // compact when the history reaches this share of the context
	charsPerTok = 3    // rough, on the safe side (code and non-English text are dense)
	keepTail    = 6    // the latest messages stay as they are
)

// contextTokens: the model's context size (Quarry reports it; others get a common default).
// The last size Quarry reported is kept: a busy server that does not answer in time must not
// make it look small (that would compact far too early).
func (a *Agent) contextTokens(ctx context.Context) int {
	if a.Srv != nil && a.Srv.Kind == "quarry" {
		if st, err := a.Srv.Stats(ctx); err == nil && st.NCtx > 0 {
			a.nCtx, a.nCtxSrv = st.NCtx, a.Srv
			return st.NCtx
		}
		if a.nCtxSrv == a.Srv && a.nCtx > 0 {
			return a.nCtx
		}
	}
	return 32768
}

func msgChars(m llm.Message) int {
	n := len(m.Content) + len(m.Reasoning)
	for _, c := range m.ToolCalls {
		n += len(c.Function.Arguments) + len(c.Function.Name)
	}
	return n
}

// historyTokens estimates the size of the conversation in tokens
func (a *Agent) historyTokens() int {
	n := 0
	for _, m := range a.msgs {
		n += msgChars(m)
	}
	return n / charsPerTok
}

// needsCompact: the history is close to filling the context
func (a *Agent) needsCompact(ctx context.Context) bool {
	return len(a.msgs) > keepTail+2 && float64(a.historyTokens()) > compactAt*float64(a.contextTokens(ctx))
}

// tooLong: the request did not fit in the context (Quarry says so in the reply text, other
// servers with an error)
func tooLong(err error, res *llm.ChatResult) bool {
	if err != nil {
		e := strings.ToLower(err.Error())
		return strings.Contains(e, "prompt too long") ||
			(strings.Contains(e, "context") && (strings.Contains(e, "exceed") || strings.Contains(e, "too long")))
	}
	return res != nil && len(res.ToolCalls) == 0 && strings.TrimSpace(res.Content) == "[prompt too long]"
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf(" [... %d more characters]", len(s)-n)
}

// transcript writes messages as plain text for the summary, each part clipped
func transcript(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch {
		case m.Role == "tool" || strings.HasPrefix(m.Content, "[tool_result]"):
			fmt.Fprintf(&b, "TOOL RESULT: %s\n\n", clipText(strings.TrimPrefix(m.Content, "[tool_result] "), 500))
		case m.Role == "user":
			fmt.Fprintf(&b, "USER: %s\n\n", clipText(m.Content, 3000))
		case m.Role == "assistant":
			if strings.TrimSpace(m.Content) != "" {
				fmt.Fprintf(&b, "ASSISTANT: %s\n\n", clipText(m.Content, 1500))
			}
			for _, c := range m.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
				parts := []string{}
				for k, v := range args {
					parts = append(parts, fmt.Sprintf("%s=%s", k, clipText(fmt.Sprint(v), 200)))
				}
				fmt.Fprintf(&b, "ASSISTANT CALLED %s(%s)\n\n", c.Function.Name, strings.Join(parts, ", "))
			}
		}
	}
	return b.String()
}

const summaryPrompt = `Below is the transcript of an agent session in a terminal, which has become too long for the model's context. Write the summary that will replace it, so the work can continue seamlessly without the transcript. Include:
1. The user's task and every instruction or preference they gave, as precisely as possible (quote the important parts).
2. What has been done so far: files created or changed (with paths), and exactly how far the work got (for example: which chapters or parts are finished).
3. Conventions and decisions to keep following (naming, file layout, style, terminology choices).
4. What remains to be done, and the very next step.
Plain text, at most 500 words. Reply with only the summary.

TRANSCRIPT:
`

// Compact replaces the older part of the conversation with a summary written by the model and
// starts a new session file for it (the old one stays as it was). Returns how many messages
// were folded into the summary.
func (a *Agent) Compact(ctx context.Context) (int, error) {
	if a.Srv == nil {
		return 0, errors.New("no model selected")
	}
	if len(a.msgs) <= keepTail+2 {
		return 0, errors.New("nothing to compact yet")
	}
	// the tail: the latest messages, never starting with a tool result (it needs its call)
	cut := len(a.msgs) - keepTail
	for cut > 1 && a.msgs[cut].Role == "tool" {
		cut--
	}
	old, tail := a.msgs[1:cut], append([]llm.Message(nil), a.msgs[cut:]...)
	if len(old) == 0 {
		return 0, errors.New("nothing to compact yet")
	}

	// the transcript has to fit, with room for the answer: keep its start (the request) and its end
	ctxTok := a.contextTokens(ctx)
	budget := int(float64(ctxTok)*0.6) * charsPerTok
	t := transcript(old)
	if len(t) > budget {
		head := budget / 5
		t = t[:head] + "\n[... middle of the session left out ...]\n\n" + t[len(t)-(budget-head):]
	}
	a.Send(Info{fmt.Sprintf("compacting: the model is summarizing %d earlier messages…", len(old))})
	res, err := a.Srv.Chat(ctx, a.Model, []llm.Message{
		{Role: "system", Content: "You write precise handover summaries of agent sessions."},
		{Role: "user", Content: summaryPrompt + t},
	}, nil, false, func(llm.StreamEvent) {})
	if err != nil {
		return 0, fmt.Errorf("summarizing failed: %w", err)
	}
	summary := strings.TrimSpace(res.Content)
	if summary == "" || tooLong(nil, res) {
		return 0, errors.New("summarizing failed: empty reply")
	}

	// big tool output in the tail (a whole chapter read back) is cut down as well
	for i := range tail {
		if (tail[i].Role == "tool" || strings.HasPrefix(tail[i].Content, "[tool_result]")) && i < len(tail)-2 {
			tail[i].Content = clipText(tail[i].Content, 2000)
		}
		tail[i].Reasoning = ""
	}
	head := "[working directory: " + a.Dir + "]\n" +
		"[This session was compacted to fit the model's context. Summary of everything before the latest messages:]\n\n" +
		summary + "\n\n[The latest messages follow. Continue the work from there.]"
	msgs := []llm.Message{a.msgs[0], {Role: "user", Content: head}}
	if tail[0].Role == "user" {
		tail[0].Content = head + "\n\n" + tail[0].Content
		msgs = msgs[:1]
	}
	a.msgs = append(msgs, tail...)
	a.sessionPath = newSessionPath()
	a.Save()
	a.logf("compact", summary)
	return len(old), nil
}
