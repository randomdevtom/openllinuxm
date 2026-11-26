package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/randomdevtom/openllinuxm/internal/agent"
)

// ---------------- welcome ----------------

func (m *model) welcome() string {
	tux := []string{"    .--.    ", "   |o_o |   ", "   |:_/ |   ", "  //   \\ \\  ", " (|     | ) ", "/'\\_   _/`\\", "\\___)=(___/ "}
	var art []string
	for i, l := range tux {
		art = append(art, lipgloss.NewStyle().Foreground(lipgloss.Color(palette[i%len(palette)])).Render(l))
	}
	w := min(max(30, m.w-24), 60)
	info := lipgloss.JoinVertical(lipgloss.Left,
		gradient("openLLinuxM", true)+sDim.Render("  v"+Version),
		sDim.Render("local AI · Linux soul"),
		"",
		sDim.Render("cwd   ")+sText.Render(truncate(agent.Tildify(m.agent.Dir), w-6)),
		sDim.Render("model ")+m.modelLabel(),
		"",
		sFaint.Render("/ commands · ? shortcuts · /goal <task> to run until done"),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBrand2).Padding(0, 2).
		Render(lipgloss.JoinHorizontal(lipgloss.Center, strings.Join(art, "\n"), "   ", info))
	return box
}

func (m *model) modelLabel() string {
	a := m.agent
	if a.Srv == nil {
		return sDim.Render("none")
	}
	name := a.Model
	if a.Srv.Kind == "quarry" && a.Srv.Label != "" {
		name = strings.TrimSuffix(a.Srv.Label, ".gguf")
	}
	return sText.Render(name) + sDim.Render(" @ "+a.Srv.Name)
}

// ---------------- live line above the input ----------------

func (m *model) activity() string {
	if !m.busy {
		return ""
	}
	now := time.Now()
	star := lipgloss.NewStyle().Foreground(cBrand).Render(starFrames[int(now.UnixMilli()/150)%len(starFrames)])
	parts := []string{fmt.Sprintf("%ds", int(time.Since(m.turnStart).Seconds()))}
	switch {
	case m.running != "":
		parts = append(parts, "running "+m.running)
	case m.draft != "":
		parts = append(parts, draftLabel(m.draft))
	case m.firstTok.IsZero():
		parts = append(parts, "reading prompt")
	default:
		if b := m.last(kAssistant); b != nil && b.text == "" && b.reasoning != "" && !b.done {
			parts = append(parts, "thinking")
		}
	}
	if m.turnTokens > 0 {
		parts = append(parts, "↓ "+kfmt(m.turnTokens)+" tokens")
	}
	parts = append(parts, "esc to interrupt")
	return " " + star + " " + shimmer(m.verb+"…", now) + sFaint.Render(" ("+strings.Join(parts, " · ")+")")
}

// draftLabel describes a tool call while the model writes it: "writing Write · 120 lines"
func draftLabel(d string) string {
	name := ""
	if i := strings.Index(d, "<function="); i >= 0 { // Qwen XML calls
		name = d[i+len("<function="):]
		if j := strings.IndexAny(name, ">\n"); j >= 0 {
			name = name[:j]
		} else {
			name = ""
		}
	}
	s := "writing a tool call"
	if name != "" {
		s = "writing " + toolTitle(name)
	}
	if n := strings.Count(d, "\n"); n > 2 {
		s += fmt.Sprintf(" · %d lines", n)
	}
	return s
}

// ---------------- input, menus, prompts ----------------

func (m *model) input() string {
	if m.confirm != nil {
		return m.confirmBox()
	}
	border := cFaint
	if !m.busy {
		border = cAccent
	}
	prompt := lipgloss.NewStyle().Foreground(cUser).Bold(true).Render("❯ ")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Width(m.w - 2).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.ta.View()))
}

func (m *model) confirmBox() string {
	c := m.confirm
	title := toolTitle(c.Tool)
	switch c.Tool {
	case "run":
		title = "Bash command"
	case "delete_file":
		title = "Delete file"
	}
	if strings.HasPrefix(c.Prompt, "run a risky command") {
		title = "Risky command"
	}
	opts := []string{"Yes", "Yes, and stop asking (yolo for this session)", "No, stop and let me say what to do (esc)"}
	if c.Tool == "delete_file" || strings.HasPrefix(c.Prompt, "run a risky command") {
		opts = []string{"Yes", "No, stop and let me say what to do (esc)"}
	}
	rows := []string{sWarnText.Bold(true).Render("⚠ " + title), "", "  " + sText.Render(wrap(c.Detail, m.w-10)), "", sText.Render("Allow it?")}
	for i, o := range opts {
		line := fmt.Sprintf("  %d. %s", i+1, o)
		if i == m.confirmSel {
			line = lipgloss.NewStyle().Foreground(cAccent).Render(fmt.Sprintf("❯ %d. %s", i+1, o))
		}
		rows = append(rows, line)
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cWarn).Padding(0, 1).Width(m.w - 2).
		Render(strings.Join(rows, "\n"))
}

// slash-command menu, shown while the input is "/something" with no space yet
func (m *model) slashMenu() string {
	items := m.slashMatches()
	if len(items) == 0 {
		return ""
	}
	var rows []string
	for i, c := range items {
		name := fmt.Sprintf("%-10s", c.name)
		line := "  " + sText.Render(name) + "  " + sDim.Render(c.desc)
		if i == m.slashSel {
			line = lipgloss.NewStyle().Foreground(cAccent).Render("❯ "+name) + "  " + sText.Render(c.desc)
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

func (m *model) shortcuts() string {
	col := func(k, d string) string { return sText.Render(fmt.Sprintf("%-12s", k)) + sDim.Render(d) }
	left := []string{col("/", "commands"), col("enter", "send"), col("alt+enter", "newline"), col("↑ ↓", "history")}
	right := []string{col("esc", "interrupt · clear input"), col("shift+tab", "yolo on/off"), col("ctrl+t", "show thinking"), col("ctrl+o", "expand tool output")}
	var rows []string
	for i := range left {
		rows = append(rows, "  "+left[i]+"    "+right[i])
	}
	return strings.Join(rows, "\n")
}

func (m *model) footer() string {
	var left string
	switch {
	case m.quitArmed:
		left = sDim.Render("  Press Ctrl-C again to exit")
	case m.agent.Yolo:
		left = sWarnText.Render("  ⚡ yolo: commands run without asking") + sFaint.Render(" (shift+tab)")
	default:
		left = "  " + gradient("◆ openLLinuxM", false) + sFaint.Render("  ? shortcuts")
	}
	var parts []string
	if m.agent.Srv != nil {
		parts = append(parts, m.modelLabel())
		if m.agent.Think {
			parts = append(parts, lipgloss.NewStyle().Foreground(cBrand2).Render("think on"))
		}
	}
	if st := m.stats; st != nil && m.agent.Srv != nil && m.agent.Srv.Kind == "quarry" {
		if st.CtxUsed > 0 {
			parts = append(parts, sDim.Render(fmt.Sprintf("ctx %s/%s", kfmt(st.CtxUsed), kfmt(st.NCtx))))
		}
		if st.CacheHit > 0 {
			parts = append(parts, sDim.Render(fmt.Sprintf("cache %.0f%%", st.CacheHit*100)))
		}
		if st.Last.GenS > 0 && st.Last.GenTokens > 1 {
			parts = append(parts, sDim.Render(fmt.Sprintf("%.1f tok/s", float64(st.Last.GenTokens)/st.Last.GenS)))
		}
	} else if m.lastRate > 0 {
		parts = append(parts, sDim.Render(fmt.Sprintf("~%.1f tok/s", m.lastRate)))
	}
	right := strings.Join(parts, sFaint.Render(" · ")) + " "
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

// the lines under the transcript: activity, input, menu or shortcuts, footer
func (m *model) bottom() []string {
	var out []string
	if a := m.activity(); a != "" {
		out = append(out, a)
	} else {
		out = append(out, "")
	}
	out = append(out, m.input())
	switch {
	case m.confirm != nil:
	case m.slashMenu() != "":
		out = append(out, m.slashMenu())
	case m.showKeys:
		out = append(out, m.shortcuts())
	}
	out = append(out, m.footer())
	return out
}

func (m *model) bottomHeight() int {
	h := 0
	for _, s := range m.bottom() {
		h += lipgloss.Height(s)
	}
	return h
}

// ---------------- model picker ----------------

func (m *model) picker() string {
	items := m.filtered()
	var rows []string
	rows = append(rows, gradient("◆ Choose a model", true), sDim.Render("local servers on this machine"), "")
	if m.pickFilter != "" {
		rows = append(rows, sDim.Render("filter: ")+sText.Render(m.pickFilter), "")
	}
	start := max(0, m.pickSel-(m.h-14))
	lastSrv := ""
	for i := start; i < len(items) && len(rows) < m.h-6; i++ {
		it := items[i]
		if it.srv.URL != lastSrv {
			lastSrv = it.srv.URL
			lbl := it.srv.Name + "  " + it.srv.URL
			if it.srv.Label != "" {
				lbl += "  " + it.srv.Label
			}
			rows = append(rows, sFaint.Render(lbl))
		}
		line := "   " + sText.Render(it.model)
		if i == m.pickSel {
			line = lipgloss.NewStyle().Foreground(cAccent).Render(" ❯ " + it.model)
		}
		rows = append(rows, line)
	}
	if len(items) == 0 {
		rows = append(rows, sDim.Render("no match"))
	}
	rows = append(rows, "", sFaint.Render("↑↓ move · type to filter · enter select · esc back"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2).Width(min(m.w-2, 80)).
		Render(strings.Join(rows, "\n"))
}

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	switch m.mode {
	case modeDiscover:
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(cBrand).Render(starFrames[int(time.Now().UnixMilli()/150)%len(starFrames)])+" "+shimmer("Looking for local models…", time.Now()))
	case modePick:
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, m.picker())
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{m.vp.View()}, m.bottom()...)...)
}
