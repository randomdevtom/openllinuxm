package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/randomdevtom/openllinuxm/internal/tools"
)

type blockKind int

const (
	kUser blockKind = iota
	kAssistant
	kTool
	kInfo
	kError
)

type block struct {
	kind              blockKind
	text, reasoning   string
	name, summary     string
	args              map[string]any
	result            string
	ok, done          bool
	thinkStart        time.Time
	thinkDur          time.Duration
	cache             string
	cacheW            int
	cacheThink, dirty bool
	cacheExpand       bool
	at                time.Time
	tw, rw            incWrap // streaming text and reasoning, wrapped as they arrive
}

// incWrap wraps a growing text: finished lines are wrapped once and kept, so each frame
// only wraps the new part (wrapping the whole reply every frame froze the UI on long replies).
type incWrap struct {
	w    int
	used int // bytes of the source already wrapped (up to a newline)
	out  []string
}

func (iw *incWrap) get(s string, w int) string {
	if iw.w != w || iw.used > len(s) {
		*iw = incWrap{w: w}
	}
	if i := strings.LastIndexByte(s, '\n'); i >= iw.used {
		for _, l := range strings.Split(s[iw.used:i], "\n") {
			iw.out = append(iw.out, wrap(l, w))
		}
		iw.used = i + 1
	}
	return strings.Join(append(iw.out[:len(iw.out):len(iw.out)], wrap(s[iw.used:], w)), "\n")
}

// how each tool is shown: Bash(ls -la), Write(main.go) ...
var toolTitles = map[string]string{
	"run": "Bash", "read_file": "Read", "write_file": "Write", "append_file": "Append",
	"edit_file": "Update", "list_files": "List", "delete_file": "Delete",
}

func toolTitle(name string) string {
	if t, ok := toolTitles[name]; ok {
		return t
	}
	return name
}

const (
	bullet = "⏺ "
	elbow  = "  ⎿  "
	inset  = "     " // continuation lines under the elbow
)

// collapsed tool output shows this many lines; ctrl+o shows up to expandedLines
const (
	collapsedLines = 4
	expandedLines  = 400
)

// render draws one block. Blocks are separated by a blank line (see refresh).
func (m *model) render(b *block) string {
	w := max(20, m.w-2)
	switch b.kind {
	case kUser:
		text := wrap(b.text, w-4)
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			p := "  "
			if i == 0 {
				p = "> "
			}
			lines[i] = p + l
		}
		return sUser.Width(w).Render(strings.Join(lines, "\n"))

	case kAssistant:
		var out []string
		if b.reasoning != "" && (b.text != "" || b.done) {
			dur := b.thinkDur
			if dur == 0 && !b.thinkStart.IsZero() {
				dur = time.Since(b.thinkStart)
			}
			if m.showThink {
				r := strings.TrimSpace(b.reasoning)
				out = append(out, thoughtLine(dur, "ctrl+t to hide"), sThink.Render(indent(b.rw.get(r, w-6), elbow, inset)))
			} else {
				out = append(out, thoughtLine(dur, "ctrl+t to show"))
			}
		}
		if t := strings.TrimSpace(b.text); t != "" {
			if i := strings.Index(t, "<tool_call>"); i >= 0 {
				// raw tool-call markup (the server could not parse it, e.g. the reply was cut off):
				// show what came before it and one line instead of the markup
				n := strings.Count(t[i:], "\n") + 1
				t = strings.TrimSpace(t[:i])
				if t != "" {
					out = append(out, withBullet(sText.Render(wrap(t, w-2)), sText))
				}
				if b.done {
					out = append(out, sWarnText.Render(fmt.Sprintf("%sunfinished tool call (%d lines), not run", bullet, n)))
				} else {
					out = append(out, sDim.Render(fmt.Sprintf("%swriting a tool call… (%d lines)", bullet, n)))
				}
			} else if b.done && m.md != nil {
				r, err := m.md.Render(t)
				if err != nil {
					r = wrap(t, w-2)
				}
				out = append(out, withBullet(strings.Trim(r, "\n"), sText))
			} else {
				out = append(out, withBullet(sText.Render(b.tw.get(t+" ▍", w-2)), sText))
			}
		}
		return strings.Join(out, "\n")

	case kTool:
		return m.renderTool(b, w)

	case kInfo:
		return sDim.Render(indent(wrap(b.text, w-6), elbow, inset))

	case kError:
		return sErrText.Render(bullet) + sErrText.Render(wrap("Error: "+b.text, w-2))
	}
	return ""
}

// withBullet puts ⏺ on the first non-empty line and indents the rest to match
func withBullet(s string, st lipgloss.Style) string {
	lines := strings.Split(s, "\n")
	first := true
	for i, l := range lines {
		plain := strings.TrimLeft(stripANSI(l), " ")
		if first && plain != "" {
			lines[i] = st.Render(bullet) + trimLeadingSpaces(l, 2)
			first = false
		} else if !first {
			lines[i] = "  " + trimLeadingSpaces(l, 2)
		}
	}
	return strings.Join(lines, "\n")
}

// trimLeadingSpaces drops up to n leading spaces (glamour indents its output by 2)
func trimLeadingSpaces(s string, n int) string {
	// the spaces may sit after colour codes; only strip plain leading spaces
	for i := 0; i < n && strings.HasPrefix(s, " "); i++ {
		s = s[1:]
	}
	return s
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *model) renderTool(b *block, w int) string {
	// ⏺ Bash(ls -la)
	dot := sOKText.Render(bullet)
	switch {
	case !b.done:
		if time.Now().UnixMilli()/500%2 == 0 {
			dot = sDim.Render(bullet)
		} else {
			dot = sText.Render(bullet)
		}
	case !b.ok:
		dot = sErrText.Render(bullet)
	}
	arg := b.summary
	if b.name == "edit_file" || b.name == "write_file" {
		arg = tools.Str(b.args["path"])
	}
	head := dot + sBoldText.Render(toolTitle(b.name)) + sText.Render("("+truncate(arg, max(10, w-len(toolTitle(b.name))-6))+")")
	if !b.done {
		return head + "\n" + sDim.Render(elbow+"Running…")
	}
	res := strings.TrimRight(b.result, "\n")
	if res == "the user declined this action" {
		return head + "\n" + sWarnText.Render(elbow+"Declined")
	}
	if !b.ok && b.name != "run" {
		return head + "\n" + sErrText.Render(indent(wrap(strings.TrimPrefix(res, "error: "), w-6), elbow, inset))
	}
	limit := collapsedLines
	if m.expand {
		limit = expandedLines
	}
	var body []string
	switch b.name {
	case "read_file":
		body = []string{sDim.Render(fmt.Sprintf("Read %d lines", strings.Count(res, "\n")+1))}
	case "write_file":
		content := tools.Str(b.args["content"])
		n := strings.Count(content, "\n") + 1
		body = append([]string{sText.Render(fmt.Sprintf("Wrote %d lines to %s", n, tools.Str(b.args["path"])))},
			numbered(content, limit, w-10)...)
	case "append_file":
		content := tools.Str(b.args["content"])
		body = []string{sDim.Render(fmt.Sprintf("Added %d lines to %s", strings.Count(content, "\n")+1, tools.Str(b.args["path"])))}
	case "edit_file":
		old, nw := tools.Str(b.args["old"]), tools.Str(b.args["new"])
		body = append([]string{sText.Render(fmt.Sprintf("Updated %s with %s and %s", tools.Str(b.args["path"]),
			plural(lineCount(nw), "addition"), plural(lineCount(old), "removal")))}, diff(old, nw, limit*3, w-8)...)
	case "delete_file":
		body = []string{sDim.Render("Deleted " + tools.Str(b.args["path"]))}
	case "run":
		code, out := "0", res
		if strings.HasPrefix(res, "exit ") {
			first, rest, _ := strings.Cut(res, "\n")
			code, out = strings.TrimPrefix(first, "exit "), rest
		}
		body = clipLines(out, limit, w-8, sDim)
		if code != "0" {
			body = append([]string{sErrText.Render("exit " + code)}, body...)
		}
	default:
		body = clipLines(res, limit, w-8, sDim)
	}
	return head + "\n" + indent(strings.Join(body, "\n"), elbow, inset)
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// clipLines: the first n lines, each cut to w columns, and a "+N lines" note
func clipLines(s string, n, w int, st lipgloss.Style) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out []string
	for i, l := range lines {
		if i == n {
			out = append(out, sFaint.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", len(lines)-n)))
			break
		}
		out = append(out, st.Render(truncate(strings.ReplaceAll(l, "\t", "    "), w)))
	}
	return out
}

// numbered: a preview of new file content with line numbers
func numbered(s string, n, w int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out []string
	for i, l := range lines {
		if i == n {
			out = append(out, sFaint.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", len(lines)-n)))
			break
		}
		out = append(out, sFaint.Render(fmt.Sprintf("%4d ", i+1))+sDim.Render(truncate(strings.ReplaceAll(l, "\t", "    "), w-5)))
	}
	return out
}

// diff: removed lines on red, added on green (the edit is one exact snippet, so its old and
// new text are shown whole, minus the lines they share at the start and end)
func diff(old, nw string, n, w int) []string {
	a, b := strings.Split(old, "\n"), strings.Split(nw, "\n")
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []string
	add := func(st lipgloss.Style, sign, l string) {
		out = append(out, st.Render(truncate(sign+" "+strings.ReplaceAll(l, "\t", "    "), w)))
	}
	if pre > 0 {
		add(sDim, " ", a[pre-1])
	}
	for _, l := range a[pre : len(a)-suf] {
		add(sDel, "-", l)
	}
	for _, l := range b[pre : len(b)-suf] {
		add(sAdd, "+", l)
	}
	if suf > 0 {
		add(sDim, " ", a[len(a)-suf])
	}
	if len(out) > n {
		more := len(out) - n
		out = append(out[:n], sFaint.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", more)))
	}
	return out
}

func (m *model) add(b *block) *block {
	b.dirty, b.at = true, time.Now()
	m.blocks = append(m.blocks, b)
	return b
}

func (m *model) last(kind blockKind) *block {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == kind {
			return m.blocks[i]
		}
	}
	return nil
}

func (m *model) info(s string) { m.add(&block{kind: kInfo, text: s, done: true}); m.refresh(true) }

func (m *model) fail(s string) { m.add(&block{kind: kError, text: s, done: true}); m.refresh(true) }

// ---------------- the live line above the input ----------------

// verbs for the activity line, one per turn
var thinkVerbs = []string{"Thinking", "Pondering", "Mulling", "Cogitating", "Noodling", "Ruminating",
	"Deliberating", "Musing", "Scheming", "Percolating", "Brewing", "Untangling", "Reckoning", "Tinkering",
	"Conjuring", "Simmering", "Hatching", "Puzzling"}

var starFrames = []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"}

// thoughtLine: the folded thinking once it is over
func thoughtLine(dur time.Duration, hint string) string {
	s := sDim.Render(fmt.Sprintf("✻ Thought for %ds", int(dur.Seconds()+0.5)))
	if hint != "" {
		s += sFaint.Render(" (" + hint + ")")
	}
	return s
}

// shimmer colours a word with a bright band sweeping across it
func shimmer(word string, now time.Time) string {
	rs := []rune(word)
	pos := float64(now.UnixMilli()%1600)/1600*float64(len(rs)+8) - 4
	var sb strings.Builder
	for i, r := range rs {
		d := float64(i) - pos
		if d < 0 {
			d = -d
		}
		c := cThink
		switch {
		case d < 1:
			c = cThinkHi
		case d < 2.5:
			c = cThinkMid
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(c).Render(string(r)))
	}
	return sb.String()
}

// indent prefixes the first line with first and the rest with rest
func indent(s, first, rest string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = rest + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
