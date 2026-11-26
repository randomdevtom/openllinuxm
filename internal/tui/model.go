// Package tui is the Bubble Tea interface: transcript, input, model picker, status bar.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/randomdevtom/openllinuxm/internal/agent"
	"github.com/randomdevtom/openllinuxm/internal/config"
	"github.com/randomdevtom/openllinuxm/internal/llm"
)

type mode int

const (
	modeDiscover mode = iota
	modePick
	modeChat
)

type model struct {
	agent   *agent.Agent
	cfg     *config.Config
	servers []*llm.Server
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
	confirm     *agent.Confirm
	stats       *llm.QuarryStats
	quitArmed   bool
	history     []string
	histPos     int
	pickItems   []pickItem
	pickFilter  string
	pickSel     int
	discoverErr string

	verb          string             // word on the activity line, one per turn
	turnTokens    int                // tokens streamed this turn
	running       string             // tool running now
	draft         string             // the tool call the model is writing now (raw text)
	typeSeq       int                // bumped on every input change
	prefilled     string             // text last sent for type-ahead
	prefillCancel context.CancelFunc // stops the type-ahead request in flight
	expand        bool               // ctrl+o: show tool output in full
	showKeys      bool               // "?" shortcuts panel
	confirmSel    int                // highlighted choice in the permission prompt
	slashSel      int                // highlighted command in the slash menu
}

var debugLog *os.File

func init() {
	if p := os.Getenv("OPENLLINUXM_DEBUG"); p != "" {
		debugLog, _ = os.Create(p)
	}
}

type serversMsg []*llm.Server

type statsMsg struct{ st *llm.QuarryStats }

type statsTick struct{}

// typed: the input stopped changing (type-ahead check); seq tells stale ticks apart
type typedTick struct{ seq int }

// New makes the interface for an agent.
func New(a *agent.Agent, cfg *config.Config) tea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything · / for commands"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = sDim
	ta.BlurredStyle = ta.FocusedStyle
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	ta.Focus()
	sp := spinner.New(spinner.WithSpinner(spinner.Spinner{Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}, FPS: time.Second / 12}))
	sp.Style = lipgloss.NewStyle().Foreground(cAccent)
	m := &model{agent: a, cfg: cfg, ta: ta, spin: sp, vp: viewport.New(80, 20), showThink: cfg.ShowThinking}
	m.replay()
	return m
}

// replay draws a continued session's history as transcript blocks.
func (m *model) replay() {
	msgs := m.agent.Messages()
	if len(msgs) <= 1 {
		return
	}
	var pending *block // last tool block still waiting for its result
	for _, msg := range msgs[1:] {
		switch msg.Role {
		case "user":
			if strings.HasPrefix(msg.Content, "[tool_result] ") && pending != nil {
				pending.result, pending.ok, pending.done = strings.TrimPrefix(msg.Content, "[tool_result] "), true, true
				pending = nil
				continue
			}
			t := msg.Content
			if strings.HasPrefix(t, "[working directory: ") {
				if _, rest, ok := strings.Cut(t, "]\n"); ok {
					t = rest
				}
			}
			if strings.HasPrefix(t, "GOAL: ") {
				t = "/goal " + strings.SplitN(strings.TrimPrefix(t, "GOAL: "), "\nWork autonomously", 2)[0]
			}
			m.add(&block{kind: kUser, text: t, done: true})
		case "assistant":
			if msg.Content != "" || msg.Reasoning != "" {
				m.add(&block{kind: kAssistant, text: msg.Content, reasoning: msg.Reasoning, done: true})
			}
			for _, c := range msg.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
				if args == nil {
					args = map[string]any{}
				}
				pending = m.add(&block{kind: kTool, name: c.Function.Name, summary: m.agent.CallSummary(c.Function.Name, args), args: args})
			}
		case "tool":
			if pending != nil {
				out := msg.Content
				failed := strings.HasPrefix(out, "error") || (strings.HasPrefix(out, "exit ") && !strings.HasPrefix(out, "exit 0\n") && out != "exit 0")
				pending.result, pending.ok, pending.done = out, !failed, true
				pending = nil
			}
		}
	}
	for _, b := range m.blocks {
		b.done = true
	}
	m.add(&block{kind: kInfo, text: fmt.Sprintf("continued session · %d messages", len(msgs)-1), done: true})
}

func (m *model) Init() tea.Cmd {
	extra := []string{}
	if m.cfg.URL != "" {
		extra = append(extra, m.cfg.URL)
	}
	return tea.Batch(textarea.Blink, m.spin.Tick, func() tea.Msg {
		// the saved server first: scanning every port takes seconds when one is busy
		if m.cfg.URL != "" && m.cfg.Model != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			s, err := llm.Probe(ctx, "Local server", m.cfg.URL)
			cancel()
			if err == nil {
				for _, mo := range s.Models {
					if mo == m.cfg.Model {
						return serversMsg{s}
					}
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return serversMsg(llm.Discover(ctx, extra))
	})
}

func (m *model) layout() {
	if m.w == 0 {
		return
	}
	atBottom := m.vp.AtBottom() || m.vp.TotalLineCount() == 0 // stay pinned when the input area grows
	m.ta.SetWidth(m.w - 6)
	m.vp.Width = m.w
	m.vp.Height = max(3, m.h-m.bottomHeight())
	if m.mdW != m.w-4 {
		m.mdW = m.w - 4
		st := styles.DarkStyleConfig
		zero := uint(0)
		st.Document.Margin = &zero // the ⏺ bullet is the margin
		m.md, _ = glamour.NewTermRenderer(glamour.WithStyles(st), glamour.WithWordWrap(m.mdW), glamour.WithEmoji())
		for _, b := range m.blocks {
			b.dirty = true
		}
	}
	m.refresh(atBottom)
}

// refresh re-renders the transcript; only changed blocks are rendered again.
func (m *model) refresh(forceBottom bool) {
	atBottom := m.vp.AtBottom() || forceBottom
	parts := []string{m.welcome()}
	for _, b := range m.blocks {
		if b.dirty || b.cacheW != m.w || b.cacheThink != m.showThink || b.cacheExpand != m.expand || !b.done {
			b.cache = m.render(b)
			b.cacheW, b.cacheThink, b.cacheExpand, b.dirty = m.w, m.showThink, m.expand, false
		}
		if b.cache != "" {
			parts = append(parts, b.cache)
		}
	}
	m.vp.SetContent(strings.Join(parts, "\n\n") + "\n")
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *model) statsCmd() tea.Cmd {
	srv := m.agent.Srv
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
			if h := max(3, m.h-m.bottomHeight()); h != m.vp.Height {
				m.layout()
			}
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

	case typedTick:
		m.typeAhead(msg.seq)
		return m, nil

	case agent.AssistantStart:
		m.draft = ""
		m.add(&block{kind: kAssistant})
		m.refresh(true)
		return m, nil

	case agent.Delta:
		b := m.last(kAssistant)
		if b == nil {
			b = m.add(&block{kind: kAssistant})
		}
		if m.firstTok.IsZero() {
			m.firstTok = time.Now()
		}
		m.chunks++
		m.turnTokens++
		if msg.Tool != "" {
			m.draft += msg.Tool
			return m, nil
		}
		if msg.Reasoning != "" {
			if b.thinkStart.IsZero() {
				b.thinkStart = time.Now()
			}
			b.reasoning += msg.Reasoning
		}
		if msg.Text != "" {
			if b.thinkDur == 0 && !b.thinkStart.IsZero() {
				b.thinkDur = time.Since(b.thinkStart)
			}
			b.text += msg.Text
		}
		return m, nil // redrawn by the spinner tick, at most 12 times a second

	case agent.AssistantDone:
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

	case agent.ToolStart:
		m.draft = ""
		m.add(&block{kind: kTool, name: msg.Name, summary: msg.Summary, args: msg.Args})
		m.running = toolTitle(msg.Name)
		m.refresh(true)
		return m, nil

	case agent.ToolResult:
		if b := m.last(kTool); b != nil {
			b.result, b.ok, b.done, b.dirty = msg.Out, msg.OK, true, true
		}
		m.running = ""
		m.refresh(false)
		return m, nil

	case agent.Confirm:
		c := msg
		m.confirm, m.confirmSel = &c, 0
		m.layout()
		return m, nil

	case agent.Info:
		m.info(msg.Text)
		return m, nil

	case agent.TurnDone:
		m.busy, m.cancel, m.confirm, m.running = false, nil, nil, ""
		for _, b := range m.blocks {
			if !b.done {
				b.done, b.dirty = true, true
			}
		}
		switch {
		case msg.Cancelled:
			m.info("Interrupted · what should it do instead?")
		case msg.Err != nil:
			m.fail(msg.Err.Error())
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
