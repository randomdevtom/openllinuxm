package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	palette   = []string{"#00f5d4", "#00d4f5", "#00a3f5", "#4d7cff", "#7c5cff", "#b45cff"}
	cAccent   = lipgloss.Color("#4d7cff")
	cUser     = lipgloss.Color("#b45cff")
	cOK       = lipgloss.Color("#3ddc97")
	cErr      = lipgloss.Color("#ff5c7a")
	cWarn     = lipgloss.Color("#ffb454")
	cDim      = lipgloss.Color("#6b7280")
	cFaint    = lipgloss.Color("#3a3f4b")
	cText     = lipgloss.Color("#d6dae3")
	sDim      = lipgloss.NewStyle().Foreground(cDim)
	sFaint    = lipgloss.NewStyle().Foreground(cFaint)
	sBold     = lipgloss.NewStyle().Bold(true).Foreground(cText)
	cThink    = lipgloss.Color("#d97757") // the thinking line: warm orange, brighter band
	cThinkMid = lipgloss.Color("#eb9a73")
	cThinkHi  = lipgloss.Color("#ffd2b8")
	sText     = lipgloss.NewStyle().Foreground(cText)
	sBoldText = lipgloss.NewStyle().Foreground(cText).Bold(true)
	sOKText   = lipgloss.NewStyle().Foreground(cOK)
	sErrText  = lipgloss.NewStyle().Foreground(cErr)
	sWarnText = lipgloss.NewStyle().Foreground(cWarn)
	sUser     = lipgloss.NewStyle().Foreground(lipgloss.Color("#c4c8d4")).Background(lipgloss.Color("#262a35"))
	sDel      = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd7dc")).Background(lipgloss.Color("#4a1f28"))
	sAdd      = lipgloss.NewStyle().Foreground(lipgloss.Color("#d4ffe4")).Background(lipgloss.Color("#1d4030"))
	sThink    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b93a7")).Italic(true)
	sKey      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9aa4ba")).Background(lipgloss.Color("#272b35")).Padding(0, 1)
	sBar      = lipgloss.NewStyle().Background(lipgloss.Color("#161922"))
	sToolTag  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00d4f5"))
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

func wrap(s string, w int) string { return lipgloss.NewStyle().Width(w).Render(s) }

func kfmt(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
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
