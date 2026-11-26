package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/randomdevtom/openllinuxm/internal/llm"
	"github.com/randomdevtom/openllinuxm/internal/tools"
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
		"Prefer edit_file to rewriting files. " +
		"When the user asks you to do something that a tool or a terminal command can do, do it yourself with the tools; " +
		"never tell the user to run a command themselves. Only ask first for actions that are destructive or need their password. " +
		"Be brief. Answer in Markdown."
	if textTools {
		p += tools.TextPrompt
	}
	return p
}

// Events sent from the agent goroutine to the UI (through Agent.Send).
type (
	AssistantStart struct{}
	Delta          struct{ Text, Reasoning, Tool string } // Tool: a tool call being written (progress)
	AssistantDone  struct {
		Content string
		Chunks  int
		Dur     time.Duration
	}
	ToolStart struct {
		Name, Summary string
		Args          map[string]any // the call's arguments, for previews and diffs
	}
	ToolResult struct {
		Out string
		OK  bool
	}
	Confirm struct {
		Prompt string
		Tool   string   // tool name
		Detail string   // the command or path
		Reply  chan int // 0 deny, 1 allow, 2 allow all (this session)
	}
	Info     struct{ Text string }
	TurnDone struct {
		Err       error
		Cancelled bool
	}
)

// Agent holds one conversation and runs its turns against a server.
type Agent struct {
	Srv       *llm.Server
	Model     string
	Think     bool
	Yolo      bool
	Dir       string // working directory for tools
	Send      func(any)
	textTools bool
	msgs      []llm.Message
	log       *os.File
	// where the conversation is saved after every step
	sessionPath string
}

func New(dir string) *Agent {
	a := &Agent{Dir: dir}
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
	a.msgs = []llm.Message{{Role: "system", Content: systemPrompt(a.textTools)}}
	a.sessionPath = newSessionPath()
}

func (a *Agent) logf(role, text string) {
	if a.log != nil {
		fmt.Fprintf(a.log, "[%s] %s: %s\n", time.Now().Format(time.RFC3339), role, text)
	}
}

// summary of a call for the transcript: the most telling argument
func (a *Agent) callSummary(name string, args map[string]any) string {
	path := tools.Str(args["path"])
	if filepath.IsAbs(path) {
		if r, err := filepath.Rel(a.Dir, path); err == nil && !strings.HasPrefix(r, "..") {
			path = r
		} else {
			path = Tildify(path)
		}
	}
	switch name {
	case "run":
		return tools.Str(args["command"])
	case "web_search":
		return tools.Str(args["query"])
	case "web_fetch":
		return tools.Str(args["url"])
	case "edit_file":
		return fmt.Sprintf("%s  (-%d +%d lines)", path, strings.Count(tools.Str(args["old"]), "\n")+1, strings.Count(tools.Str(args["new"]), "\n")+1)
	case "write_file":
		return fmt.Sprintf("%s  (%d lines)", path, strings.Count(tools.Str(args["content"]), "\n")+1)
	}
	return path
}

// Tildify writes paths under the home directory as ~/...
func Tildify(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

// ExpandDir resolves ~ and relative paths against base.
func ExpandDir(p, base string) string {
	if strings.HasPrefix(p, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// Turn runs one user message to completion: model replies, tool calls, results, repeat.
func (a *Agent) Turn(ctx context.Context, user string, goal bool) {
	if goal {
		user = "GOAL: " + user + "\nWork autonomously with the tools: do, verify, fix. When the goal is fully done, reply GOAL_COMPLETED and a short summary."
	}
	if len(a.msgs) == 1 {
		user = "[working directory: " + a.Dir + "]\n" + user
	}
	a.msgs = append(a.msgs, llm.Message{Role: "user", Content: user})
	a.logf("user", user)
	defer a.Save()
	limit, nudges := maxHops, 0
	if goal {
		limit = maxGoalHops
	}
	for hop := 0; hop < limit; hop++ {
		a.Send(AssistantStart{})
		t0 := time.Now()
		var defs []any
		if !a.textTools {
			defs = tools.Defs()
		}
		res, err := a.Srv.Chat(ctx, a.Model, a.msgs, defs, a.Think, func(e llm.StreamEvent) {
			a.Send(Delta{e.Text, e.Reasoning, e.Tool})
		})
		if errors.Is(err, llm.ErrToolsUnsupported) {
			a.textTools = true
			a.msgs[0].Content = systemPrompt(true)
			a.Send(Info{"this model has no native tool calling: using text tool blocks"})
			hop--
			continue
		}
		if ctx.Err() != nil {
			a.Send(TurnDone{Cancelled: true})
			return
		}
		if err != nil {
			a.Send(TurnDone{Err: err})
			return
		}
		a.Send(AssistantDone{res.Content, res.Chunks, time.Since(t0)})
		a.logf("assistant", res.Content)

		calls := res.ToolCalls
		content := res.Content
		if a.textTools {
			tc, perr := tools.ParseText(res.Content)
			if perr != nil {
				a.msgs = append(a.msgs, llm.Message{Role: "assistant", Content: content},
					llm.Message{Role: "user", Content: "[tool_result] error: " + perr.Error()})
				continue
			}
			if tc != nil {
				calls = []llm.ToolCall{*tc}
			}
		}
		if len(calls) == 0 && res.Finish == "length" {
			// cut off at the reply limit, usually in the middle of a long tool call: keep the
			// history short and ask for the work in smaller pieces
			a.Send(Info{"reply hit the length limit: asking the model to write in smaller parts"})
			a.msgs = append(a.msgs, llm.Message{Role: "assistant", Content: "(reply cut off at the length limit)"},
				llm.Message{Role: "user", Content: "Your last reply was cut off at the length limit before it finished. Redo it in smaller steps: write_file at most ~150 lines, then append_file the rest in parts."})
			continue
		}
		if len(calls) == 0 {
			a.msgs = append(a.msgs, llm.Message{Role: "assistant", Content: content, Reasoning: res.Reasoning})
			if goal && !strings.Contains(content, "GOAL_COMPLETED") && nudges < 3 {
				nudges++
				a.msgs = append(a.msgs, llm.Message{Role: "user", Content: "Not done yet? Continue with the next concrete step. If the goal is complete, reply GOAL_COMPLETED and a summary."})
				continue
			}
			a.Send(TurnDone{})
			return
		}
		nudges = 0
		if a.textTools {
			a.msgs = append(a.msgs, llm.Message{Role: "assistant", Content: content})
		} else {
			a.msgs = append(a.msgs, llm.Message{Role: "assistant", Content: content, Reasoning: res.Reasoning, ToolCalls: calls})
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
				a.Send(ToolStart{Name: c.Function.Name, Summary: a.callSummary(c.Function.Name, args), Args: args})
				allowed := true
				if need, why := tools.NeedsConfirm(c.Function.Name, args, a.Yolo); need {
					reply := make(chan int, 1)
					a.Send(Confirm{Prompt: why, Tool: c.Function.Name, Detail: a.callSummary(c.Function.Name, args), Reply: reply})
					select {
					case r := <-reply:
						allowed = r > 0
						if r == 2 {
							a.Yolo = true
						}
					case <-ctx.Done():
						a.Send(TurnDone{Cancelled: true})
						return
					}
				}
				if allowed {
					out, ok = tools.Run(ctx, a.Dir, c.Function.Name, args)
				} else {
					out = "the user declined this action"
				}
			}
			a.Send(ToolResult{out, ok})
			a.logf("tool", c.Function.Name+" "+c.Function.Arguments+" -> "+out)
			if a.textTools {
				a.msgs = append(a.msgs, llm.Message{Role: "user", Content: "[tool_result] " + out})
			} else {
				a.msgs = append(a.msgs, llm.Message{Role: "tool", Content: out, ToolCallID: c.ID})
			}
			a.Save()
			if ctx.Err() != nil {
				a.Send(TurnDone{Cancelled: true})
				return
			}
		}
	}
	a.Send(Info{fmt.Sprintf("stopped after %d steps (limit)", limit)})
	a.Send(TurnDone{})
}
