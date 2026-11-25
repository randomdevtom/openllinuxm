package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	maxHops     = 25 // tool calls in one normal turn
	maxGoalHops = 60 // in /goal mode
)

// systemPrompt is short and the same in every folder: local servers cache the prompt prefix,
// so the working directory goes in the first user message instead.
func systemPrompt(textTools bool) string {
	p := "You are openLLinuxM, a coding assistant in the user's Linux terminal. " +
		"Use the tools to inspect, edit and run things; paths are relative to the working directory. " +
		"Prefer edit_file to rewriting files. Be brief. Answer in Markdown."
	if textTools {
		p += textToolPrompt
	}
	return p
}

// events from the agent goroutine to the UI
type (
	evAssistantStart struct{}
	evDelta          struct{ text, reasoning string }
	evAssistantDone  struct {
		content string
		chunks  int
		dur     time.Duration
	}
	evTool       struct{ name, summary string }
	evToolResult struct {
		out string
		ok  bool
	}
	evConfirm struct {
		prompt string
		reply  chan int // 0 deny, 1 allow, 2 allow all (this session)
	}
	evInfo     struct{ text string }
	evTurnDone struct {
		err       error
		cancelled bool
	}
)

type Agent struct {
	srv       *Server
	model     string
	think     bool
	yolo      bool
	dir       string
	textTools bool
	msgs      []Message
	send      func(tea.Msg)
	log       *os.File
}

func NewAgent(dir string) *Agent {
	a := &Agent{dir: dir}
	a.Reset()
	if st, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(st, ".local", "state", "openllinuxm")
		if os.MkdirAll(p, 0o755) == nil {
			a.log, _ = os.OpenFile(filepath.Join(p, "log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		}
	}
	return a
}

func (a *Agent) Reset() {
	a.msgs = []Message{{Role: "system", Content: systemPrompt(a.textTools)}}
}

func (a *Agent) logf(role, text string) {
	if a.log != nil {
		fmt.Fprintf(a.log, "[%s] %s: %s\n", time.Now().Format(time.RFC3339), role, text)
	}
}

// summary of a call for the transcript: the most telling argument
func (a *Agent) callSummary(name string, args map[string]any) string {
	path := str(args["path"])
	if filepath.IsAbs(path) {
		if r, err := filepath.Rel(a.dir, path); err == nil && !strings.HasPrefix(r, "..") {
			path = r
		} else {
			path = tildify(path)
		}
	}
	switch name {
	case "run":
		return str(args["command"])
	case "edit_file":
		return fmt.Sprintf("%s  (-%d +%d lines)", path, strings.Count(str(args["old"]), "\n")+1, strings.Count(str(args["new"]), "\n")+1)
	case "write_file":
		return fmt.Sprintf("%s  (%d lines)", path, strings.Count(str(args["content"]), "\n")+1)
	}
	return path
}

// Turn runs one user message to completion: model replies, tool calls, results, repeat.
func (a *Agent) Turn(ctx context.Context, user string, goal bool) {
	if goal {
		user = "GOAL: " + user + "\nWork autonomously with the tools: do, verify, fix. When the goal is fully done, reply GOAL_COMPLETED and a short summary."
	}
	if len(a.msgs) == 1 {
		user = "[working directory: " + a.dir + "]\n" + user
	}
	a.msgs = append(a.msgs, Message{Role: "user", Content: user})
	a.logf("user", user)
	limit, nudges := maxHops, 0
	if goal {
		limit = maxGoalHops
	}
	for hop := 0; hop < limit; hop++ {
		a.send(evAssistantStart{})
		t0 := time.Now()
		var tools []any
		if !a.textTools {
			tools = toolDefs()
		}
		res, err := a.srv.Chat(ctx, a.model, a.msgs, tools, a.think, func(e StreamEvent) {
			a.send(evDelta{e.Text, e.Reasoning})
		})
		if errors.Is(err, ErrToolsUnsupported) {
			a.textTools = true
			a.msgs[0].Content = systemPrompt(true)
			a.send(evInfo{"this model has no native tool calling: using text tool blocks"})
			hop--
			continue
		}
		if ctx.Err() != nil {
			a.send(evTurnDone{cancelled: true})
			return
		}
		if err != nil {
			a.send(evTurnDone{err: err})
			return
		}
		a.send(evAssistantDone{res.Content, res.Chunks, time.Since(t0)})
		a.logf("assistant", res.Content)

		calls := res.ToolCalls
		content := res.Content
		if a.textTools {
			tc, perr := parseTextTool(res.Content)
			if perr != nil {
				a.msgs = append(a.msgs, Message{Role: "assistant", Content: content},
					Message{Role: "user", Content: "[tool_result] error: " + perr.Error()})
				continue
			}
			if tc != nil {
				calls = []ToolCall{*tc}
			}
		}
		if len(calls) == 0 && res.Finish == "length" {
			// cut off at the reply limit, usually in the middle of a long tool call: keep the
			// history short and ask for the work in smaller pieces
			a.send(evInfo{"reply hit the length limit: asking the model to write in smaller parts"})
			a.msgs = append(a.msgs, Message{Role: "assistant", Content: "(reply cut off at the length limit)"},
				Message{Role: "user", Content: "Your last reply was cut off at the length limit before it finished. Redo it in smaller steps: write_file at most ~150 lines, then append_file the rest in parts."})
			continue
		}
		if len(calls) == 0 {
			a.msgs = append(a.msgs, Message{Role: "assistant", Content: content})
			if goal && !strings.Contains(content, "GOAL_COMPLETED") && nudges < 3 {
				nudges++
				a.msgs = append(a.msgs, Message{Role: "user", Content: "Not done yet? Continue with the next concrete step. If the goal is complete, reply GOAL_COMPLETED and a summary."})
				continue
			}
			a.send(evTurnDone{})
			return
		}
		nudges = 0
		if a.textTools {
			a.msgs = append(a.msgs, Message{Role: "assistant", Content: content})
		} else {
			a.msgs = append(a.msgs, Message{Role: "assistant", Content: content, ToolCalls: calls})
		}
		for _, c := range calls {
			var args map[string]any
			out, ok := "", false
			if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil && strings.TrimSpace(c.Function.Arguments) != "" {
				out = "error: arguments are not valid JSON: " + err.Error()
			} else {
				if args == nil {
					args = map[string]any{}
				}
				a.send(evTool{c.Function.Name, a.callSummary(c.Function.Name, args)})
				allowed := true
				if need, why := NeedsConfirm(c.Function.Name, args, a.yolo); need {
					reply := make(chan int, 1)
					a.send(evConfirm{why, reply})
					select {
					case r := <-reply:
						allowed = r > 0
						if r == 2 {
							a.yolo = true
						}
					case <-ctx.Done():
						a.send(evTurnDone{cancelled: true})
						return
					}
				}
				if allowed {
					out, ok = RunTool(ctx, a.dir, c.Function.Name, args)
				} else {
					out = "the user declined this action"
				}
			}
			a.send(evToolResult{out, ok})
			a.logf("tool", c.Function.Name+" "+c.Function.Arguments+" -> "+out)
			if a.textTools {
				a.msgs = append(a.msgs, Message{Role: "user", Content: "[tool_result] " + out})
			} else {
				a.msgs = append(a.msgs, Message{Role: "tool", Content: out, ToolCallID: c.ID})
			}
			if ctx.Err() != nil {
				a.send(evTurnDone{cancelled: true})
				return
			}
		}
	}
	a.send(evInfo{fmt.Sprintf("stopped after %d steps (limit)", limit)})
	a.send(evTurnDone{})
}
