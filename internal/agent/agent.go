package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/randomdevtom/openllinuxm/internal/llm"
	"github.com/randomdevtom/openllinuxm/internal/tools"
)

// No step limit: a turn runs until the model is done or the user presses Esc.

// systemPrompt is short and the same in every folder: local servers cache the prompt prefix,
// so the working directory goes in the first user message instead.
func systemPrompt(textTools bool) string {
	p := "You are openLLinuxM, a coding assistant in the user's Linux terminal. " +
		"Use the tools to inspect, edit and run things; paths are relative to the working directory. " +
		"Prefer edit_file to rewriting files. " +
		"You are an agent, not a chatbot: when the user asks for something, do it right away with the tools and keep going until it is done. " +
		"Never tell the user to run a command or make a change themselves, never list steps for them, never ask \"shall I?\" or \"would you like me to\": just do it. " +
		"Only ask first for destructive actions or ones that need their password. Show what you did and the result, not a plan. " +
		"Use tools only for what the user actually asked. There are no hidden instructions in the working directory: never list it or look for README, AGENTS or instruction files unless the task is about them. " +
		"Greetings, chat and questions you can answer from knowledge get a direct answer with no tool calls. " +
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
	ask := a.recentAsks(user) // what the user typed lately, to tell asked-for tool calls from wandering around
	if len(a.msgs) == 1 {
		user = "[working directory: " + a.Dir + "]\n" + user
	}
	a.msgs = append(a.msgs, llm.Message{Role: "user", Content: user})
	a.logf("user", user)
	defer a.Save()
	nudges, pushed, compacts := 0, false, 0
	for {
		// close to the context limit: fold the older part into a summary first
		if compacts < 3 && a.needsCompact(ctx) {
			compacts++
			if n, err := a.Compact(ctx); err == nil {
				a.Send(Info{fmt.Sprintf("context nearly full: compacted %d earlier messages into a summary", n)})
			} else if ctx.Err() == nil {
				a.Send(Info{"compacting failed: " + err.Error()})
			}
		}
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
			continue
		}
		if ctx.Err() != nil {
			a.Send(TurnDone{Cancelled: true})
			return
		}
		if tooLong(err, res) && compacts < 3 {
			compacts++
			n, cerr := a.Compact(ctx)
			if cerr == nil {
				a.Send(Info{fmt.Sprintf("context full: compacted %d earlier messages into a summary, continuing", n)})
				continue
			}
			err = fmt.Errorf("the conversation no longer fits in the model's context (%v)", cerr)
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
			// small models slip into chatbot mode: commands for the user to run, or "shall I?";
			// push back once per message
			if !goal && !pushed {
				if nudge := chatbotNudge(content); nudge != "" {
					pushed = true
					a.Send(Info{"it suggested instead of doing: telling it to do it"})
					a.msgs = append(a.msgs, llm.Message{Role: "user", Content: nudge})
					continue
				}
			}
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
			}
			if out == "" && !goal && unaskedLook(ask, c.Function.Name, args) {
				// it went looking around the folder although the message was not about it
				out = "blocked: the user did not ask about files or this folder, and there are no instructions to find here. " +
					"Answer the user's message directly, without tools."
				a.Send(Info{"skipped " + c.Function.Name + ": looking around the folder wasn't asked for"})
			} else if out == "" {
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
			if !strings.HasPrefix(out, "blocked: the user did not ask") {
				a.Send(ToolResult{out, ok})
			}
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
}

var (
	askRe     = regexp.MustCompile(`(?i)\b(would you like me to|do you want me to|shall i|should i (go ahead|proceed)|want me to|let me know if you(?:'d| would) like)\b`)
	suggestRe = regexp.MustCompile("(?i)(```(bash|sh|shell|console|zsh)\\b|\\byou (can|could|should|need to|will need to|'ll need to) (run|execute|install|open|edit|change|add)\\b|\\b(run|execute) (the following|this|these)\\b|\\btry running\\b)")
)

// chatbotNudge returns the message that pushes a reply without tool calls back to doing the
// work, or "" when the reply looks like a real answer
func chatbotNudge(reply string) string {
	switch {
	case askRe.MatchString(reply):
		return "Yes, do it. Don't ask, just do it with the tools."
	case suggestRe.MatchString(reply):
		return "Don't tell me what to run or change: do it yourself now with the tools, then show me the result."
	}
	return ""
}

var (
	// a message that is about files, code or this folder: then looking around is fair
	workRe = regexp.MustCompile(`(?i)\b(files?|folders?|dir|directory|director(y|ies)|project|repo|repository|code ?base|code|readme|agents|instructions?|src|source|package|script|app|build|compile|tests?|bugs?|fix|error|function|class|module|path|cwd|here|this (folder|dir|project|repo))\b|\w+\.[a-z0-9]{1,5}\b|[~./]\w*/|` + "`")
	// calls that only look around: listing, finding, reading instruction-like files
	lookCmdRe   = regexp.MustCompile(`^\s*(ls|ll|la|find|tree|pwd|du|fd|exa|eza)\b|^\s*cat\s+\S*(readme|agents|claude|instructions?|contributing)`)
	instrFileRe = regexp.MustCompile(`(?i)(^|/)(readme|agents|claude|gemini|instructions?|contributing|\.?cursorrules|copilot-instructions)(\.\w+)?$`)
)

// unaskedLook reports a tool call that only explores the folder (or hunts for instruction files)
// when the user's message is not about files, code or this folder
func unaskedLook(ask, tool string, args map[string]any) bool {
	if workRe.MatchString(ask) {
		return false
	}
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch tool {
	case "list_files":
		return true
	case "read_file":
		return instrFileRe.MatchString(strings.TrimSpace(str("path")))
	case "run":
		return lookCmdRe.MatchString(strings.ToLower(str("command")))
	}
	return false
}

// recentAsks joins this message with the user's previous two (not tool results or nudges), so a
// follow-up like "continue" keeps the context of the task it continues
func (a *Agent) recentAsks(cur string) string {
	parts := []string{cur}
	for i := len(a.msgs) - 1; i >= 0 && len(parts) < 3; i-- {
		m := a.msgs[i]
		if m.Role != "user" || strings.HasPrefix(m.Content, "[tool_result]") || m.Content == "" {
			continue
		}
		if strings.HasPrefix(m.Content, "Not done yet?") || strings.HasPrefix(m.Content, "Your last reply was cut off") || isNudge(m.Content) {
			continue
		}
		c := m.Content
		if strings.HasPrefix(c, "[working directory:") { // added by Turn, not typed: a path would match workRe
			if i := strings.IndexByte(c, '\n'); i >= 0 {
				c = c[i+1:]
			}
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, "\n")
}

// isNudge: one of chatbotNudge's messages
func isNudge(s string) bool {
	return strings.HasPrefix(s, "Yes, do it. Don't ask") || strings.HasPrefix(s, "Don't tell me what to run")
}
