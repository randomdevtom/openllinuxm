package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/randomdevtom/openllinuxm/internal/agent"
	"github.com/randomdevtom/openllinuxm/internal/llm"
)

// slash commands, for the menu and /help
type slashCmd struct{ name, desc string }

var slashCmds = []slashCmd{
	{"/help", "show commands and keys"},
	{"/model", "switch model or server"},
	{"/clear", "start a new chat"},
	{"/goal", "work on a task until it is done"},
	{"/think", "turn reasoning on or off"},
	{"/yolo", "run commands without asking"},
	{"/cd", "change the working directory"},
	{"/tools", "list the tools the model can use"},
	{"/exit", "quit"},
}

func (m *model) slashMatches() []slashCmd {
	v := m.ta.Value()
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \n") || m.busy {
		return nil
	}
	var out []slashCmd
	for _, c := range slashCmds {
		if strings.HasPrefix(c.name, v) {
			out = append(out, c)
		}
	}
	if len(out) == 1 && out[0].name == v {
		return nil // typed in full
	}
	return out
}

type pickItem struct {
	srv   *llm.Server
	model string
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
	m.agent.Srv, m.agent.Model = it.srv, it.model
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
			if m.agent.Srv != nil {
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
		// choices: Yes (1), Yes + don't ask again (2, not for deletes and risky commands), No (0)
		replies := []int{1, 2, 0}
		if m.confirm.Tool == "delete_file" || strings.HasPrefix(m.confirm.Prompt, "run a risky command") {
			replies = []int{1, 0}
		}
		r := -1
		switch s {
		case "up", "k", "shift+tab":
			m.confirmSel = max(0, m.confirmSel-1)
		case "down", "j", "tab":
			m.confirmSel = min(len(replies)-1, m.confirmSel+1)
		case "enter":
			r = replies[m.confirmSel]
		case "y", "Y":
			r = 1
		case "n", "N", "esc", "ctrl+c":
			r = 0
		case "1", "2", "3":
			if i := int(s[0] - '1'); i < len(replies) {
				r = replies[i]
			}
		}
		if r >= 0 {
			m.confirm.Reply <- r
			m.confirm = nil
			if r == 0 && m.cancel != nil {
				// "No, and tell it what to do differently": stop the turn, back to the input
				m.cancel()
				go m.agent.Srv.CancelRemote()
			}
			m.layout()
		}
		return m, nil
	}
	// slash menu navigation
	if items := m.slashMatches(); len(items) > 0 {
		switch s {
		case "up", "ctrl+p":
			m.slashSel = (m.slashSel - 1 + len(items)) % len(items)
			return m, nil
		case "down", "ctrl+n":
			m.slashSel = (m.slashSel + 1) % len(items)
			return m, nil
		case "tab", "enter":
			c := items[min(m.slashSel, len(items)-1)]
			m.slashSel = 0
			if s == "enter" && c.name != "/goal" && c.name != "/cd" {
				m.ta.Reset()
				m.layout()
				return m, m.submit(c.name)
			}
			m.ta.SetValue(c.name + " ")
			m.ta.CursorEnd()
			m.layout()
			return m, nil
		}
	}
	switch s {
	case "ctrl+d":
		return m, tea.Quit
	case "ctrl+c", "esc":
		if m.busy && m.cancel != nil {
			m.cancel()
			go m.agent.Srv.CancelRemote()
			return m, nil
		}
		if s == "esc" {
			m.ta.Reset()
			m.showKeys = false
			m.layout()
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
	case "shift+tab":
		m.agent.Yolo = !m.agent.Yolo
		return m, nil
	case "ctrl+o":
		m.expand = !m.expand
		m.refresh(false)
		return m, nil
	case "?":
		if m.ta.Value() == "" {
			m.showKeys = !m.showKeys
			m.layout()
			return m, nil
		}
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
	before := m.ta.Value()
	var c tea.Cmd
	m.ta, c = m.ta.Update(k)
	if m.ta.Value() != before {
		m.slashSel, m.showKeys = 0, false
		m.typeSeq++
		seq := m.typeSeq
		c = tea.Batch(c, tea.Tick(700*time.Millisecond, func(time.Time) tea.Msg { return typedTick{seq} }))
	}
	// grow the input with its content, up to 8 lines
	if h := min(8, max(1, m.ta.LineCount())); h != m.ta.Height() {
		m.ta.SetHeight(h)
	}
	m.layout()
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
			var b strings.Builder
			for _, c := range slashCmds {
				fmt.Fprintf(&b, "%-8s %s\n", c.name, c.desc)
			}
			b.WriteString("\nenter send · alt+enter newline · esc interrupt · shift+tab auto-accept · ctrl+t thinking · ctrl+o expand · pgup/pgdn scroll")
			m.info(b.String())
			return nil
		case "/model", "/models", "/server":
			m.mode, m.pickSel, m.pickFilter = modePick, 0, ""
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				return serversMsg(llm.Discover(ctx, []string{m.cfg.URL}))
			}
		case "/clear", "/new":
			m.blocks = nil
			m.agent.Reset()
			m.refresh(true)
			return nil
		case "/think":
			m.agent.Think = arg != "off" && (arg == "on" || !m.agent.Think)
			m.cfg.Think = m.agent.Think
			m.cfg.Save()
			m.info(fmt.Sprintf("reasoning %s", map[bool]string{true: "on", false: "off"}[m.agent.Think]))
			return nil
		case "/yolo":
			m.agent.Yolo = !m.agent.Yolo
			m.info(fmt.Sprintf("run commands without asking: %v (deletes and risky commands always ask)", m.agent.Yolo))
			return nil
		case "/tools":
			m.info("read_file · write_file · append_file · edit_file · list_files · delete_file (asks) · web_search · web_fetch · run (asks unless yolo)")
			return nil
		case "/cd":
			if arg != "" {
				m.agent.Dir = agent.ExpandDir(arg, m.agent.Dir)
				m.agent.Reset()
				m.info("working directory: " + m.agent.Dir + " (new chat)")
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
	if m.agent.Srv == nil {
		m.fail("no model selected · /model")
		return nil
	}
	label := text
	if goal {
		label = "/goal " + text
	}
	m.add(&block{kind: kUser, text: label, done: true})
	m.busy, m.turnStart, m.quitArmed, m.turnTokens, m.showKeys = true, time.Now(), false, 0, false
	m.verb = thinkVerbs[int(time.Now().UnixNano()/1e6)%len(thinkVerbs)]
	m.stopTypeAhead()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.refresh(true)
	go m.agent.Turn(ctx, text, goal)
	return tea.Batch(m.spin.Tick, m.statsCmd(), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return statsTick{} }))
}

// ---------------- view ----------------

// typeAhead: once typing pauses, let Quarry read the message so far (see agent.Prefill)
func (m *model) typeAhead(seq int) {
	text := strings.TrimSpace(m.ta.Value())
	if seq != m.typeSeq || m.busy || m.confirm != nil || len(text) < 12 || strings.HasPrefix(text, "/") || text == m.prefilled {
		return
	}
	m.stopTypeAhead()
	m.prefilled = text
	ctx, cancel := context.WithCancel(context.Background())
	m.prefillCancel = cancel
	go m.agent.Prefill(ctx, text)
}

func (m *model) stopTypeAhead() {
	if m.prefillCancel != nil {
		m.prefillCancel()
		m.prefillCancel = nil
	}
	m.prefilled = ""
}
