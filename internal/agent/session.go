package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/randomdevtom/openllinuxm/internal/llm"
)

// Sessions: every conversation is saved after each step to
// ~/.local/state/openllinuxm/sessions/<start time>.json, so it can be continued later (-c).

type sessionFile struct {
	Dir       string        `json:"dir"`
	Model     string        `json:"model"`
	TextTools bool          `json:"text_tools,omitempty"`
	Saved     time.Time     `json:"saved"`
	Messages  []llm.Message `json:"messages"`
}

func stateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "openllinuxm")
}

func SessionsDir() string { return filepath.Join(stateDir(), "sessions") }

func newSessionPath() string {
	return filepath.Join(SessionsDir(), time.Now().Format("20060102-150405")+".json")
}

// Save writes the conversation to its session file (nothing before the first message).
func (a *Agent) Save() error {
	if len(a.msgs) <= 1 || a.sessionPath == "" {
		return nil
	}
	b, err := json.Marshal(sessionFile{Dir: a.Dir, Model: a.Model, TextTools: a.textTools, Saved: time.Now(), Messages: a.msgs})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.sessionPath), 0o700); err != nil {
		return err
	}
	tmp := a.sessionPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.sessionPath)
}

// LatestSession returns the most recently saved session file.
func LatestSession() (string, error) {
	files, _ := filepath.Glob(filepath.Join(SessionsDir(), "*.json"))
	if len(files) == 0 {
		return "", fmt.Errorf("no saved sessions in %s", SessionsDir())
	}
	sort.Slice(files, func(i, j int) bool {
		si, _ := os.Stat(files[i])
		sj, _ := os.Stat(files[j])
		return si.ModTime().After(sj.ModTime())
	})
	return files[0], nil
}

// Load continues a saved session: its messages, working directory and file.
func (a *Agent) Load(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var f sessionFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(f.Messages) == 0 || f.Messages[0].Role != "system" {
		f.Messages = append([]llm.Message{{Role: "system", Content: systemPrompt(f.TextTools)}}, f.Messages...)
	}
	a.msgs, a.textTools, a.sessionPath = f.Messages, f.TextTools, path
	if st, err := os.Stat(f.Dir); err == nil && st.IsDir() {
		a.Dir = f.Dir
	}
	return nil
}

// Messages is the conversation so far (the first one is the system prompt).
func (a *Agent) Messages() []llm.Message { return a.msgs }

// CallSummary is the one-line description of a tool call shown in the transcript.
func (a *Agent) CallSummary(name string, args map[string]any) string {
	return a.callSummary(name, args)
}

var logHeader = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\dT[^\]]+)\] (user|assistant|tool): `)

// ImportLog rebuilds a session from log.txt entries written at or after since (for
// conversations from before sessions were saved). Reasoning is not in the log, so it is lost.
func ImportLog(logPath string, since time.Time) (string, int, error) {
	fh, err := os.Open(logPath)
	if err != nil {
		return "", 0, err
	}
	defer fh.Close()
	type entry struct{ role, text string }
	var entries []entry
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	keep := false
	for sc.Scan() {
		line := sc.Text()
		if m := logHeader.FindStringSubmatch(line); m != nil {
			t, err := time.Parse(time.RFC3339, m[1])
			keep = err == nil && !t.Before(since)
			if keep {
				entries = append(entries, entry{m[2], line[len(m[0]):]})
			}
			continue
		}
		if keep && len(entries) > 0 {
			entries[len(entries)-1].text += "\n" + line
		}
	}
	if err := sc.Err(); err != nil {
		return "", 0, err
	}
	if len(entries) == 0 {
		return "", 0, fmt.Errorf("no log entries since %s", since.Format(time.RFC3339))
	}
	msgs := []llm.Message{{Role: "system", Content: systemPrompt(false)}}
	dir := ""
	n := 0
	for _, e := range entries {
		switch e.role {
		case "user":
			if dir == "" && strings.HasPrefix(e.text, "[working directory: ") {
				if i := strings.Index(e.text, "]"); i > 0 {
					dir = e.text[len("[working directory: "):i]
				}
			}
			msgs = append(msgs, llm.Message{Role: "user", Content: e.text})
		case "assistant":
			msgs = append(msgs, llm.Message{Role: "assistant", Content: e.text})
		case "tool":
			// "<name> <arguments JSON> -> <output>"
			name, rest, _ := strings.Cut(e.text, " ")
			dec := json.NewDecoder(strings.NewReader(rest))
			var raw json.RawMessage
			args, out := "{}", rest
			if dec.Decode(&raw) == nil {
				args = string(raw)
				out = strings.TrimPrefix(rest[dec.InputOffset():], " -> ")
			}
			var cb bytes.Buffer
			if json.Compact(&cb, []byte(args)) == nil {
				args = cb.String()
			}
			n++
			id := fmt.Sprintf("call_import_%d", n)
			last := &msgs[len(msgs)-1]
			if last.Role != "assistant" {
				msgs = append(msgs, llm.Message{Role: "assistant"})
				last = &msgs[len(msgs)-1]
			}
			tc := llm.ToolCall{ID: id, Type: "function"}
			tc.Function.Name, tc.Function.Arguments = name, args
			last.ToolCalls = append(last.ToolCalls, tc)
			msgs = append(msgs, llm.Message{Role: "tool", Content: out, ToolCallID: id})
		}
	}
	a := &Agent{msgs: msgs, Dir: dir, sessionPath: filepath.Join(SessionsDir(), since.Format("20060102-150405")+"-imported.json")}
	if err := a.Save(); err != nil {
		return "", 0, err
	}
	return a.sessionPath, len(msgs) - 1, nil
}
