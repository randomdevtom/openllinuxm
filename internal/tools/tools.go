package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/randomdevtom/openllinuxm/internal/llm"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxOut = 12000 // characters of tool output sent back to the model

// Defs: the tool schemas, kept short: every word here is in every prompt
func Defs() []any {
	fn := func(name, desc string, props map[string]any, req ...string) any {
		return map[string]any{"type": "function", "function": map[string]any{
			"name": name, "description": desc,
			"parameters": map[string]any{"type": "object", "properties": props, "required": req},
		}}
	}
	desc := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	return []any{
		fn("read_file", "Read a text file.", map[string]any{"path": desc("file path")}, "path"),
		fn("write_file", "Create or overwrite a file.", map[string]any{"path": desc("file path"), "content": desc("full content")}, "path", "content"),
		fn("append_file", "Append text to a file. Write big files in parts: write_file the first part, append_file the rest.",
			map[string]any{"path": desc("file path"), "content": desc("text to add")}, "path", "content"),
		fn("edit_file", "Replace one exact, unique snippet in a file. Prefer this to rewriting a file.",
			map[string]any{"path": desc("file path"), "old": desc("exact text to replace"), "new": desc("replacement")}, "path", "old", "new"),
		fn("list_files", "List a directory.", map[string]any{"path": desc("directory, default .")}),
		fn("delete_file", "Delete a file (the user confirms).", map[string]any{"path": desc("file path")}, "path"),
		fn("web_search", "Search the web; returns titles, URLs and snippets.",
			map[string]any{"query": desc("search terms")}, "query"),
		fn("web_fetch", "Fetch a web page as plain text.", map[string]any{"url": desc("page URL")}, "url"),
		fn("run", "Run a shell command; returns exit code and output.",
			map[string]any{"command": desc("bash command"), "timeout": map[string]any{"type": "integer", "description": "seconds, default 120"}}, "command"),
	}
}

// fallback for servers without tool calling: the same tools as a fenced JSON block
const TextPrompt = "\nTo use a tool, reply with ONLY:\n```tool\n{\"tool\":\"NAME\",...args}\n```\n" +
	"Tools: read_file{path} write_file{path,content} append_file{path,content} edit_file{path,old,new} list_files{path} delete_file{path} web_search{query} web_fetch{url} run{command,timeout}. " +
	"One tool per reply; the result comes back as [tool_result]."

var textRe = regexp.MustCompile("(?s)```tool\\s*\\n(.*?)\\n```")

// ParseText turns a ```tool block into a ToolCall (name + JSON arguments).
func ParseText(s string) (*llm.ToolCall, error) {
	m := textRe.FindStringSubmatch(s)
	if m == nil {
		return nil, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(m[1]), &obj); err != nil {
		return nil, fmt.Errorf("tool block is not valid JSON: %v", err)
	}
	name, _ := obj["tool"].(string)
	delete(obj, "tool")
	if name == "execute_command" {
		name = "run"
	}
	args, _ := json.Marshal(obj)
	tc := &llm.ToolCall{ID: fmt.Sprintf("text_%d", time.Now().UnixNano()), Type: "function"}
	tc.Function.Name, tc.Function.Arguments = name, string(args)
	return tc, nil
}

var dangerous = []string{"rm -rf /", "rm -rf ~", "rm -rf *", "mkfs", "dd if=", ":(){", "shutdown", "reboot", "poweroff", "> /dev/sd", "chmod -r 777 /", "sudo "}

// NeedsConfirm says whether a call must be approved first, and why.
func NeedsConfirm(name string, args map[string]any, yolo bool) (bool, string) {
	switch name {
	case "delete_file":
		return true, "delete " + Str(args["path"])
	case "run":
		cmd := strings.ToLower(Str(args["command"]))
		for _, d := range dangerous {
			if strings.Contains(cmd, d) {
				return true, "run a risky command: " + Str(args["command"])
			}
		}
		if !yolo {
			return true, "run: " + Str(args["command"])
		}
	}
	return false, ""
}

// Str turns a JSON value into text.
func Str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func clip(s string) string {
	if len(s) <= maxOut {
		return s
	}
	return s[:maxOut/2] + fmt.Sprintf("\n... [%d characters cut] ...\n", len(s)-maxOut) + s[len(s)-maxOut/2:]
}

// Run executes one call in dir and returns its result text (errors included).
func Run(ctx context.Context, dir, name string, args map[string]any) (string, bool) {
	p := func() string {
		path := Str(args["path"])
		if path == "" {
			path = "."
		}
		if strings.HasPrefix(path, "~/") {
			if h, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(h, path[2:])
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		return path
	}
	fail := func(err error) (string, bool) { return "error: " + err.Error(), false }
	switch name {
	case "read_file":
		b, err := os.ReadFile(p())
		if err != nil {
			return fail(err)
		}
		return clip(string(b)), true
	case "write_file":
		path, content := p(), Str(args["content"])
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fail(err)
		}
		return fmt.Sprintf("wrote %d lines", strings.Count(content, "\n")+1), true
	case "append_file":
		f, err := os.OpenFile(p(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		n, err := f.WriteString(Str(args["content"]))
		if err != nil {
			return fail(err)
		}
		return fmt.Sprintf("appended %d bytes", n), true
	case "edit_file":
		path, old, nw := p(), Str(args["old"]), Str(args["new"])
		b, err := os.ReadFile(path)
		if err != nil {
			return fail(err)
		}
		s := string(b)
		switch n := strings.Count(s, old); {
		case old == "":
			return "error: old is empty", false
		case n == 0:
			return "error: old text not found; read_file and copy it exactly", false
		case n > 1:
			return fmt.Sprintf("error: old text occurs %d times; include more context", n), false
		}
		if err := os.WriteFile(path, []byte(strings.Replace(s, old, nw, 1)), 0o644); err != nil {
			return fail(err)
		}
		return "edited " + Str(args["path"]), true
	case "list_files":
		ents, err := os.ReadDir(p())
		if err != nil {
			return fail(err)
		}
		var names []string
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "(empty)", true
		}
		return clip(strings.Join(names, "\n")), true
	case "delete_file":
		if err := os.Remove(p()); err != nil {
			return fail(err)
		}
		return "deleted " + Str(args["path"]), true
	case "run":
		t := 120
		if v, ok := args["timeout"].(float64); ok && v > 0 {
			t = int(v)
		}
		if t > 1800 {
			t = 1800
		}
		cctx, cancel := context.WithTimeout(ctx, time.Duration(t)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "bash", "-c", Str(args["command"]))
		cmd.Dir = dir
		// Output goes to a file, not a pipe: a command that starts something in the
		// background ("python3 -m http.server &") leaves that process holding the pipe,
		// and waiting for the pipe to close would hang forever. With a file, Wait
		// returns when bash exits and the background process keeps running.
		f, ferr := os.CreateTemp("", "openllinuxm-run-*")
		if ferr != nil {
			return fail(ferr)
		}
		os.Remove(f.Name()) // the open handle is enough
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
		// Own process group, so a timeout or Esc stops everything the command started.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		err := cmd.Run()
		out, _ := os.ReadFile("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		if cctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("error: timed out after %ds\n%s", t, clip(string(out))), false
		}
		res := strings.TrimRight(string(out), "\n")
		if res == "" {
			res = "(no output)"
		}
		return fmt.Sprintf("exit %d\n%s", code, clip(res)), code == 0
	case "web_search":
		q := Str(args["query"])
		if q == "" {
			return "error: query is empty", false
		}
		out, err := webSearch(ctx, q, 8)
		if err != nil {
			return fail(err)
		}
		return clip(out), true
	case "web_fetch":
		out, err := webFetch(ctx, Str(args["url"]))
		if err != nil {
			return fail(err)
		}
		return clip(out), true
	}
	return fmt.Sprintf("error: unknown tool %q", name), false
}
