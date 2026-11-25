package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ---------------- look ----------------

var (
	palette  = []string{"#00f5d4", "#00d4f5", "#00a3f5", "#4d7cff", "#7c5cff", "#b45cff"}
	cAccent  = lipgloss.Color("#4d7cff")
	cUser    = lipgloss.Color("#b45cff")
	cOK      = lipgloss.Color("#3ddc97")
	cErr     = lipgloss.Color("#ff5c7a")
	cWarn    = lipgloss.Color("#ffb454")
	cDim     = lipgloss.Color("#6b7280")
	cFaint   = lipgloss.Color("#3a3f4b")
	cText    = lipgloss.Color("#d6dae3")
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sBold    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sThink   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b93a7")).Italic(true)
	sKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("#9aa4ba")).Background(lipgloss.Color("#272b35")).Padding(0, 1)
	sBar     = lipgloss.NewStyle().Background(lipgloss.Color("#161922"))
	sToolTag = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00d4f5"))
)

func gradient(s string, bold bool) string {
	var b strings.Builder
	i := 0
	for _, r := range s {
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(palette[(i*len(palette))/max(1, len([]rune(s)))]))
		if bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(string(r)))
		i++
	}
	return b.String()
}

// ---------------- transcript ----------------

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
	result            string
	ok, done          bool
	thinkStart        time.Time
	thinkDur          time.Duration
	cache             string
	cacheW            int
	cacheThink, dirty bool
	at                time.Time
}

// ---------------- model ----------------

type mode int

const (
	modeDiscover mode = iota
	modePick
	modeChat
)

type pickItem struct {
	srv   *Server
	model string
}

type model struct {
	agent   *Agent
	cfg     *Config
	servers []*Server
	mode    mode

	w, h   int
	vp     viewport.Model
	ta     textarea.Model
	spin   spinner.Model
	md     *glamour.TermRenderer
	mdW    int
	blocks []*block

	busy        bool
	cancel      context.CancelFunc
	turnStart   time.Time
	firstTok    time.Time
	chunks      int
	lastRate    float64
	showThink   bool
	confirm     *evConfirm
	stats       *QuarryStats
	quitArmed   bool
	history     []string
	histPos     int
	pickItems   []pickItem
	pickFilter  string
	pickSel     int
	discoverErr string
}

var debugLog *os.File

func init() {
	if p := os.Getenv("OPENLLINUXM_DEBUG"); p != "" {
		debugLog, _ = os.Create(p)
	}
}

type serversMsg []*Server
type statsMsg struct{ st *QuarryStats }
type statsTick struct{}

func newModel(a *Agent, cfg *Config) *model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything · /help for commands"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(2)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = sDim
	ta.BlurredStyle = ta.FocusedStyle
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	ta.Focus()
	sp := spinner.New(spinner.WithSpinner(spinner.Spinner{Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}, FPS: time.Second / 12}))
	sp.Style = lipgloss.NewStyle().Foreground(cAccent)
	return &model{agent: a, cfg: cfg, ta: ta, spin: sp, vp: viewport.New(80, 20), showThink: cfg.ShowThinking}
}

func (m *model) Init() tea.Cmd {
	extra := []string{}
	if m.cfg.URL != "" {
		extra = append(extra, m.cfg.URL)
	}
	return tea.Batch(textarea.Blink, m.spin.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return serversMsg(Discover(ctx, extra))
	})
}

func (m *model) inputHeight() int {
	if m.confirm != nil {
		return 3
	}
	return m.ta.Height() + 2
}

func (m *model) layout() {
	if m.w == 0 {
		return
	}
	m.ta.SetWidth(m.w - 6)
	m.vp.Width = m.w
	m.vp.Height = max(3, m.h-1-1-m.inputHeight())
	if m.mdW != m.w-4 {
		m.mdW = m.w - 4
		m.md, _ = glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(m.mdW), glamour.WithEmoji())
		for _, b := range m.blocks {
			b.dirty = true
		}
	}
	m.refresh(false)
}

// refresh re-renders the transcript; only changed blocks are rendered again.
func (m *model) refresh(forceBottom bool) {
	atBottom := m.vp.AtBottom() || forceBottom
	var parts []string
	label := "\n  " + gradient("◆ openLLinuxM", true)
	needLabel := false // the first visible reply or tool line after a user message gets the name
	for _, b := range m.blocks {
		if b.kind == kUser {
			needLabel = true
		}
		if b.dirty || b.cacheW != m.w || b.cacheThink != m.showThink || !b.done {
			b.cache = m.render(b)
			b.cacheW, b.cacheThink, b.dirty = m.w, m.showThink, false
		}
		if b.cache != "" {
			if needLabel && (b.kind == kAssistant || b.kind == kTool) {
				parts = append(parts, label)
				needLabel = false
			}
			parts = append(parts, b.cache)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, m.welcome())
	}
	m.vp.SetContent(strings.Join(parts, "\n"))
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *model) welcome() string {
	tux := []string{"   .--.  ", "  |o_o | ", "  |:_/ | ", " //   \\ \\", "(|     | )", "/'\\_   _/`\\", "\\___)=(___/"}
	var lines []string
	for i, l := range tux {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(palette[i%len(palette)])).Render(l))
	}
	art := lipgloss.JoinVertical(lipgloss.Left, lines...)
	info := lipgloss.JoinVertical(lipgloss.Left,
		gradient("openLLinuxM", true),
		sDim.Render("local AI · Linux soul"),
		"",
		sDim.Render("cwd   ")+sBold.Render(truncate(tildify(m.agent.dir), max(10, m.w-40))),
		sDim.Render("model ")+sBold.Render(m.modelLabel()),
		"",
		sDim.Render("Enter send · Alt+Enter newline · Esc stop · Ctrl+T thinking · /help"),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(1, 3).
		Render(lipgloss.JoinHorizontal(lipgloss.Center, art, "    ", info))
	return lipgloss.Place(m.w, max(m.vp.Height-1, 12), lipgloss.Center, lipgloss.Center, box)
}

func (m *model) modelLabel() string {
	a := m.agent
	if a.srv == nil {
		return "none"
	}
	name := a.model
	if a.srv.Kind == "quarry" && a.srv.Label != "" {
		name = strings.TrimSuffix(a.srv.Label, ".gguf")
	}
	return name + sDim.Render(" @ "+a.srv.Name)
}

func tildify(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func wrap(s string, w int) string { return lipgloss.NewStyle().Width(w).Render(s) }

func (m *model) render(b *block) string {
	w := max(20, m.w-4)
	pad := lipgloss.NewStyle().PaddingLeft(2)
	switch b.kind {
	case kUser:
		head := lipgloss.NewStyle().Foreground(cUser).Bold(true).Render("● you") + sFaint.Render("  "+b.at.Format("15:04"))
		bubble := lipgloss.NewStyle().Background(lipgloss.Color("#2a2140")).Foreground(lipgloss.Color("#f1ecff")).
			Padding(0, 1).Width(w).Render(b.text)
		return "\n" + pad.Render(head+"\n"+bubble)
	case kAssistant:
		var out []string
		if b.reasoning != "" {
			dur := b.thinkDur
			if dur == 0 && !b.thinkStart.IsZero() {
				dur = time.Since(b.thinkStart)
			}
			head := sThink.Render(fmt.Sprintf("✻ thinking %.0fs", dur.Seconds()))
			r := strings.TrimSpace(b.reasoning)
			switch {
			case m.showThink:
				out = append(out, head, sThink.Render(wrap(r, w)))
			case b.text == "" && !b.done:
				lines := strings.Split(wrap(r, w), "\n")
				if len(lines) > 3 {
					lines = lines[len(lines)-3:]
				}
				out = append(out, head, sThink.Render(strings.Join(lines, "\n")))
			default:
				out = append(out, head+sFaint.Render(fmt.Sprintf("  · %d words · ctrl+t", len(strings.Fields(r)))))
			}
		}
		if t := strings.TrimSpace(b.text); t != "" {
			if i := strings.Index(t, "<tool_call>"); i >= 0 {
				// raw tool-call markup (the server could not parse it, e.g. the reply was cut off):
				// show what came before it and one line instead of the markup
				n := strings.Count(t[i:], "\n") + 1
				t = strings.TrimSpace(t[:i])
				note := sDim.Render(fmt.Sprintf("⚙ writing a tool call… (%d lines)", n))
				if b.done {
					note = lipgloss.NewStyle().Foreground(cWarn).Render(fmt.Sprintf("⚠ unfinished tool call (%d lines), not run", n))
				}
				if t != "" {
					out = append(out, lipgloss.NewStyle().Foreground(cText).Render(wrap(t, w)))
				}
				out = append(out, note)
			} else if b.done && m.md != nil {
				if r, err := m.md.Render(t); err == nil {
					out = append(out, strings.Trim(r, "\n"))
				} else {
					out = append(out, wrap(t, w))
				}
			} else {
				out = append(out, lipgloss.NewStyle().Foreground(cText).Render(wrap(t+" ▍", w)))
			}
		}
		if len(out) == 0 {
			return ""
		}
		return pad.Render(strings.Join(out, "\n"))
	case kTool:
		icon := m.spin.View()
		if b.done {
			icon = lipgloss.NewStyle().Foreground(cOK).Render("✓")
			if !b.ok {
				icon = lipgloss.NewStyle().Foreground(cErr).Render("✗")
			}
		}
		summ := b.summary
		if len(summ) > w-12 {
			summ = summ[:max(0, w-13)] + "…"
		}
		head := icon + " " + sToolTag.Render(b.name) + " " + sDim.Render(summ)
		res := strings.TrimRight(b.result, "\n")
		if res == "" {
			return pad.Render(head)
		}
		lines := strings.Split(res, "\n")
		more := ""
		if len(lines) > 6 {
			more = sFaint.Render(fmt.Sprintf("\n… %d more lines", len(lines)-6))
			lines = lines[:6]
		}
		for i, l := range lines {
			if len(l) > w-4 {
				lines[i] = l[:max(0, w-5)] + "…"
			}
		}
		body := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(cFaint).
			PaddingLeft(1).Foreground(cDim).Render(strings.Join(lines, "\n") + more)
		return pad.Render(head + "\n" + body)
	case kInfo:
		return pad.Render(sDim.Italic(true).Render("· " + b.text))
	case kError:
		return pad.Render(lipgloss.NewStyle().Foreground(cErr).Render("✗ " + wrap(b.text, w-2)))
	}
	return ""
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

// ---------------- update ----------------

func (m *model) statsCmd() tea.Cmd {
	srv := m.agent.srv
	if srv == nil || srv.Kind != "quarry" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		st, _ := srv.Stats(ctx)
		return statsMsg{st}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if debugLog != nil {
		fmt.Fprintf(debugLog, "%s %T %v\n", time.Now().Format("15:04:05.000"), msg, msg)
	}
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return m, nil

	case spinner.TickMsg:
		var c tea.Cmd
		m.spin, c = m.spin.Update(msg)
		if m.busy {
			m.refresh(false)
		}
		return m, c

	case serversMsg:
		m.servers = msg
		return m, m.afterDiscover()

	case statsMsg:
		if msg.st != nil {
			m.stats = msg.st
		}
		return m, nil

	case statsTick:
		if !m.busy {
			return m, nil
		}
		return m, tea.Batch(m.statsCmd(), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return statsTick{} }))

	case evAssistantStart:
		m.add(&block{kind: kAssistant})
		m.refresh(true)
		return m, nil

	case evDelta:
		b := m.last(kAssistant)
		if b == nil {
			b = m.add(&block{kind: kAssistant})
		}
		if m.firstTok.IsZero() {
			m.firstTok = time.Now()
		}
		m.chunks++
		if msg.reasoning != "" {
			if b.thinkStart.IsZero() {
				b.thinkStart = time.Now()
			}
			b.reasoning += msg.reasoning
		}
		if msg.text != "" {
			if b.thinkDur == 0 && !b.thinkStart.IsZero() {
				b.thinkDur = time.Since(b.thinkStart)
			}
			b.text += msg.text
		}
		m.refresh(false)
		return m, nil

	case evAssistantDone:
		if b := m.last(kAssistant); b != nil {
			b.done = true
			if b.thinkDur == 0 && !b.thinkStart.IsZero() {
				b.thinkDur = time.Since(b.thinkStart)
			}
			b.dirty = true
		}
		if !m.firstTok.IsZero() && m.chunks > 1 {
			if d := time.Since(m.firstTok).Seconds(); d > 0.3 {
				m.lastRate = float64(m.chunks) / d
			}
		}
		m.firstTok, m.chunks = time.Time{}, 0
		m.refresh(false)
		return m, nil

	case evTool:
		m.add(&block{kind: kTool, name: msg.name, summary: msg.summary})
		m.refresh(true)
		return m, nil

	case evToolResult:
		if b := m.last(kTool); b != nil {
			b.result, b.ok, b.done, b.dirty = msg.out, msg.ok, true, true
		}
		m.refresh(false)
		return m, nil

	case evConfirm:
		c := msg
		m.confirm = &c
		m.layout()
		return m, nil

	case evInfo:
		m.info(msg.text)
		return m, nil

	case evTurnDone:
		m.busy, m.cancel, m.confirm = false, nil, nil
		for _, b := range m.blocks {
			if !b.done {
				b.done, b.dirty = true, true
			}
		}
		switch {
		case msg.cancelled:
			m.info("stopped")
		case msg.err != nil:
			m.fail(msg.err.Error())
		}
		m.layout()
		m.refresh(true)
		return m, m.statsCmd()

	case tea.MouseMsg:
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		return m, c

	case tea.KeyMsg:
		return m.key(msg)
	}
	var c tea.Cmd
	m.ta, c = m.ta.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

func (m *model) afterDiscover() tea.Cmd {
	m.pickItems = nil
	for _, s := range m.servers {
		for _, mo := range s.Models {
			m.pickItems = append(m.pickItems, pickItem{s, mo})
		}
	}
	if len(m.pickItems) == 0 {
		m.mode = modeChat
		m.fail("No local model server found. Start Quarry, Ollama, llama.cpp or LM Studio, or pass --url http://host:port")
		return nil
	}
	// remembered or requested choice, else the only one, else ask
	for _, it := range m.pickItems {
		if it.model == m.cfg.Model && (m.cfg.URL == "" || strings.TrimRight(m.cfg.URL, "/") == it.srv.URL) {
			return m.choose(it)
		}
	}
	if len(m.pickItems) == 1 {
		return m.choose(m.pickItems[0])
	}
	m.mode, m.pickSel, m.pickFilter = modePick, 0, ""
	return nil
}

func (m *model) choose(it pickItem) tea.Cmd {
	m.agent.srv, m.agent.model = it.srv, it.model
	m.cfg.URL, m.cfg.Model = it.srv.URL, it.model
	m.cfg.Save()
	m.mode = modeChat
	for _, b := range m.blocks {
		b.dirty = true
	}
	if len(m.blocks) > 0 {
		m.info("model: " + it.model + " @ " + it.srv.Name)
	}
	m.layout()
	return m.statsCmd()
}

func (m *model) filtered() []pickItem {
	if m.pickFilter == "" {
		return m.pickItems
	}
	var out []pickItem
	f := strings.ToLower(m.pickFilter)
	for _, it := range m.pickItems {
		if strings.Contains(strings.ToLower(it.model+" "+it.srv.Name+" "+it.srv.Label), f) {
			out = append(out, it)
		}
	}
	return out
}

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.mode == modePick {
		items := m.filtered()
		switch s {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.agent.srv != nil {
				m.mode = modeChat
				m.layout()
			}
		case "up", "ctrl+p":
			m.pickSel = max(0, m.pickSel-1)
		case "down", "ctrl+n", "tab":
			m.pickSel = min(len(items)-1, m.pickSel+1)
		case "enter":
			if m.pickSel < len(items) {
				return m, m.choose(items[m.pickSel])
			}
		case "backspace":
			if m.pickFilter != "" {
				m.pickFilter = m.pickFilter[:len(m.pickFilter)-1]
				m.pickSel = 0
			}
		default:
			if len(k.Runes) > 0 {
				m.pickFilter += string(k.Runes)
				m.pickSel = 0
			}
		}
		return m, nil
	}
	if m.confirm != nil {
		r := -1
		switch s {
		case "y", "Y", "enter":
			r = 1
		case "n", "N", "esc":
			r = 0
		case "a", "A":
			r = 2
		case "ctrl+c":
			r = 0
		}
		if r >= 0 {
			m.confirm.reply <- r
			m.confirm = nil
			m.layout()
		}
		return m, nil
	}
	switch s {
	case "ctrl+d":
		return m, tea.Quit
	case "ctrl+c", "esc":
		if m.busy && m.cancel != nil {
			m.cancel()
			go m.agent.srv.CancelRemote()
			return m, nil
		}
		if s == "esc" {
			return m, nil
		}
		if m.ta.Value() != "" {
			m.ta.Reset()
			return m, nil
		}
		if m.quitArmed {
			return m, tea.Quit
		}
		m.quitArmed = true
		m.info("press Ctrl+C again to quit")
		return m, nil
	case "ctrl+t":
		m.showThink = !m.showThink
		m.cfg.ShowThinking = m.showThink
		m.cfg.Save()
		m.refresh(false)
		return m, nil
	case "ctrl+l":
		if !m.busy {
			m.blocks = nil
			m.agent.Reset()
			m.refresh(true)
		}
		return m, nil
	case "pgup", "pgdown", "ctrl+u", "ctrl+b", "shift+up", "shift+down":
		var c tea.Cmd
		switch s {
		case "ctrl+u", "ctrl+b":
			m.vp.HalfViewUp()
		case "shift+up":
			m.vp.LineUp(3)
		case "shift+down":
			m.vp.LineDown(3)
		default:
			m.vp, c = m.vp.Update(k)
		}
		return m, c
	case "up":
		if m.ta.Line() == 0 && len(m.history) > 0 && !strings.Contains(m.ta.Value(), "\n") {
			m.histPos = max(0, m.histPos-1)
			m.ta.SetValue(m.history[m.histPos])
			return m, nil
		}
	case "down":
		if len(m.history) > 0 && m.histPos < len(m.history) && !strings.Contains(m.ta.Value(), "\n") {
			m.histPos++
			if m.histPos >= len(m.history) {
				m.ta.Reset()
			} else {
				m.ta.SetValue(m.history[m.histPos])
			}
			return m, nil
		}
	case "enter":
		text := strings.TrimSpace(m.ta.Value())
		if text == "" || m.busy {
			return m, nil
		}
		m.ta.Reset()
		m.history = append(m.history, text)
		m.histPos = len(m.history)
		return m, m.submit(text)
	}
	m.quitArmed = false
	var c tea.Cmd
	m.ta, c = m.ta.Update(k)
	// grow the input with its content, up to 8 lines
	if h := min(8, max(2, m.ta.LineCount())); h != m.ta.Height() {
		m.ta.SetHeight(h)
		m.layout()
	}
	return m, c
}

func (m *model) submit(text string) tea.Cmd {
	goal := false
	if strings.HasPrefix(text, "/") {
		f := strings.Fields(text)
		arg := strings.TrimSpace(strings.TrimPrefix(text, f[0]))
		switch f[0] {
		case "/exit", "/quit", "/q":
			return tea.Quit
		case "/help", "/?":
			m.info("/model pick a model · /goal <task> work until done · /think on|off reasoning · /yolo run commands without asking · /clear new chat · /tools · /cd <dir> · /exit\n" +
				"Enter send · Alt+Enter newline · Esc stop · Ctrl+T show thinking · PgUp/PgDn scroll · Ctrl+L clear")
			return nil
		case "/model", "/models", "/server":
			m.mode, m.pickSel, m.pickFilter = modePick, 0, ""
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				return serversMsg(Discover(ctx, []string{m.cfg.URL}))
			}
		case "/clear", "/new":
			m.blocks = nil
			m.agent.Reset()
			m.refresh(true)
			return nil
		case "/think":
			m.agent.think = arg != "off" && (arg == "on" || !m.agent.think)
			m.cfg.Think = m.agent.think
			m.cfg.Save()
			m.info(fmt.Sprintf("reasoning %s", map[bool]string{true: "on", false: "off"}[m.agent.think]))
			return nil
		case "/yolo":
			m.agent.yolo = !m.agent.yolo
			m.info(fmt.Sprintf("run commands without asking: %v (deletes and risky commands always ask)", m.agent.yolo))
			return nil
		case "/tools":
			m.info("read_file · write_file · edit_file · list_files · delete_file (asks) · run (asks unless /yolo)")
			return nil
		case "/cd":
			if arg != "" {
				m.agent.dir = expandDir(arg, m.agent.dir)
				m.agent.Reset()
				m.info("working directory: " + m.agent.dir + " (new chat)")
			}
			return nil
		case "/goal":
			if arg == "" {
				m.info("usage: /goal <objective>")
				return nil
			}
			text, goal = arg, true
		default:
			m.info("unknown command " + f[0] + " · /help")
			return nil
		}
	}
	if m.agent.srv == nil {
		m.fail("no model selected · /model")
		return nil
	}
	label := text
	if goal {
		label = "🎯 " + text
	}
	m.add(&block{kind: kUser, text: label, done: true})
	m.busy, m.turnStart, m.quitArmed = true, time.Now(), false
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.refresh(true)
	go m.agent.Turn(ctx, text, goal)
	return tea.Batch(m.spin.Tick, m.statsCmd(), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return statsTick{} }))
}

// ---------------- view ----------------

func (m *model) header() string {
	left := " " + gradient("◆ openLLinuxM", true)
	right := ""
	if m.agent.srv != nil {
		th := sDim.Render("think off")
		if m.agent.think {
			th = lipgloss.NewStyle().Foreground(cWarn).Render("think on")
		}
		yolo := ""
		if m.agent.yolo {
			yolo = lipgloss.NewStyle().Foreground(cErr).Render("yolo") + sFaint.Render(" · ")
		}
		right = yolo + th + sFaint.Render(" · ") + m.modelLabel() + " "
	}
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	return sBar.Width(m.w).Render(left + strings.Repeat(" ", gap) + right)
}

func (m *model) status() string {
	var left string
	switch {
	case m.busy:
		el := time.Since(m.turnStart).Seconds()
		phase := "reading prompt"
		if !m.firstTok.IsZero() {
			phase = "writing"
			if b := m.last(kAssistant); b != nil && b.text == "" && b.reasoning != "" {
				phase = "thinking"
			}
		}
		if b := m.last(kTool); b != nil && !b.done {
			phase = "running " + b.name
		}
		left = " " + m.spin.View() + " " + lipgloss.NewStyle().Foreground(cAccent).Render(phase) + sDim.Render(fmt.Sprintf(" %.0fs", el)) + sFaint.Render("  esc to stop")
	default:
		left = " " + sKey.Render("enter") + sDim.Render(" send ") + sKey.Render("alt+enter") + sDim.Render(" newline ") + sKey.Render("ctrl+t") + sDim.Render(" thinking ") + sKey.Render("/help")
	}
	var parts []string
	if st := m.stats; st != nil && m.agent.srv != nil && m.agent.srv.Kind == "quarry" {
		parts = append(parts, fmt.Sprintf("ctx %s/%s", kfmt(st.CtxUsed), kfmt(st.NCtx)))
		if st.CacheHit > 0 {
			parts = append(parts, fmt.Sprintf("cache %.0f%%", st.CacheHit*100))
		}
		if st.Last.GenS > 0 && st.Last.GenTokens > 1 {
			parts = append(parts, fmt.Sprintf("%.1f tok/s", float64(st.Last.GenTokens)/st.Last.GenS))
		}
	} else if m.lastRate > 0 {
		parts = append(parts, fmt.Sprintf("~%.1f tok/s", m.lastRate))
	}
	right := sDim.Render(strings.Join(parts, " · ")) + " "
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	return sBar.Width(m.w).Render(left + strings.Repeat(" ", gap) + right)
}

func kfmt(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func (m *model) input() string {
	if m.confirm != nil {
		q := lipgloss.NewStyle().Foreground(cWarn).Bold(true).Render("⚠ allow? ") + sBold.Render(truncate(m.confirm.prompt, m.w-40))
		keys := "  " + sKey.Render("y") + sDim.Render(" yes ") + sKey.Render("n") + sDim.Render(" no ") + sKey.Render("a") + sDim.Render(" always")
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cWarn).Width(m.w - 2).Render(q + keys)
	}
	border := cAccent
	if m.busy {
		border = cFaint
	}
	prompt := lipgloss.NewStyle().Foreground(cUser).Bold(true).Render("❯ ")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Width(m.w - 2).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.ta.View()))
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n < 8 {
		n = 8
	}
	if len([]rune(s)) > n {
		return string([]rune(s)[:n-1]) + "…"
	}
	return s
}

func (m *model) picker() string {
	items := m.filtered()
	var rows []string
	rows = append(rows, gradient("Choose a model", true), "")
	if m.pickFilter != "" {
		rows = append(rows, sDim.Render("filter: ")+sBold.Render(m.pickFilter), "")
	}
	start := max(0, m.pickSel-(m.h-12))
	lastSrv := ""
	for i := start; i < len(items) && len(rows) < m.h-6; i++ {
		it := items[i]
		if it.srv.URL != lastSrv {
			lastSrv = it.srv.URL
			lbl := it.srv.Name + "  " + it.srv.URL
			if it.srv.Label != "" {
				lbl += "  " + it.srv.Label
			}
			rows = append(rows, sDim.Render(lbl))
		}
		line := "   " + it.model
		if i == m.pickSel {
			line = lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render(" ❯ " + it.model)
		}
		rows = append(rows, line)
	}
	if len(items) == 0 {
		rows = append(rows, sDim.Render("no match"))
	}
	rows = append(rows, "", sDim.Render("↑↓ move · type to filter · enter choose · esc back"))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return lipgloss.Place(m.w, m.h-2, lipgloss.Center, lipgloss.Center, box)
}

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	switch m.mode {
	case modeDiscover:
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			m.spin.View()+" "+gradient("looking for local models…", false))
	case modePick:
		return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.picker(), m.status())
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.vp.View(), m.input(), m.status())
}
