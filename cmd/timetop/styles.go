// Same night-shift console language as merge-medic: sharp borders, in-border
// titles, one amber accent, color otherwise reserved for state.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	bold    = lipgloss.NewStyle().Bold(true)
	dim     = lipgloss.NewStyle().Faint(true)
	green   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	amber   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	amberB  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	borderC = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	section = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, true, true, true).
		BorderForeground(lipgloss.Color("238")).
		Padding(0, 1)
)

// titledBox draws a panel with its title embedded in the top border:
// ┌─ WEEK ─ 41h20m ─────────┐
func titledBox(w int, title, meta, body string, height int, focused bool) string {
	st := section.Width(w - 2)
	bs := borderC
	if focused {
		st = st.BorderForeground(lipgloss.Color("214"))
		bs = amber
	}
	if height > 0 {
		st = st.Height(height)
	}
	// the label lives inside the top border, so it must fit inside it
	avail := w - 4
	if lipgloss.Width(title)+2 > avail {
		title = truncPlain(title, max(avail-2, 0))
		meta = ""
	} else if meta != "" && lipgloss.Width(title)+lipgloss.Width(meta)+3 > avail {
		meta = truncPlain(meta, max(avail-lipgloss.Width(title)-3, 0))
	}
	label := amberB.Render(" " + title + " ")
	if meta != "" {
		label += dim.Render(meta + " ")
	}
	rest := w - 3 - lipgloss.Width(label)
	if rest < 0 {
		rest = 0
	}
	top := bs.Render("┌─") + label + bs.Render(strings.Repeat("─", rest)+"┐")
	return top + "\n" + st.Render(body)
}

// selMark marks the cursor row with an amber bar in the row's own first cell,
// so a selected row is exactly as wide as an unselected one.
func selMark(row string) string {
	return amberB.Render("▎") + strings.TrimPrefix(row, " ")
}

// gauge renders progress against the weekly target.
func gauge(mins int, targetHours float64, width int) string {
	target := int(targetHours * 60)
	n := 0
	if target > 0 {
		n = mins * width / target
	}
	if n > width {
		n = width
	}
	filled := green
	if n >= width {
		filled = amber
	}
	return filled.Render(strings.Repeat("█", n)) + dim.Render(strings.Repeat("░", width-n))
}

// barC is bar() with the console's one accent: amber for what happened, a
// quiet rail for what did not.
func barC(mins, maxV, width int) string {
	b := bar(mins, maxV, width)
	filled := strings.Count(b, "█")
	return amber.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", width-filled))
}

// heat paints one punchcard cell: density in four steps, not a color ramp.
func heat(v, peak int) string {
	if v == 0 {
		return dim.Render("·")
	}
	steps := []string{"░", "▒", "▓", "█"}
	i := 0
	if peak > 0 {
		i = min(v*len(steps)/peak, len(steps)-1)
	}
	if i >= 3 {
		return amberB.Render(steps[i])
	}
	return amber.Render(steps[i])
}

// vbar is one column of an hour histogram, drawn in an eighth-block scale.
func vbar(v, peak int) string {
	if v == 0 {
		return dim.Render("·")
	}
	blocks := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	i := 0
	if peak > 0 {
		i = min(v*len(blocks)/peak, len(blocks)-1)
	}
	return amber.Render(blocks[i])
}

// stateStyled colors a task's standing: landed, in flight, or historical.
func stateStyled(state string) string {
	switch state {
	case "merged":
		return green.Render(fmt.Sprintf("%-7s", state))
	case "open":
		return amber.Render(fmt.Sprintf("%-7s", state))
	case "gone", "trunk", "quiet", "—":
		return dim.Render(fmt.Sprintf("%-7s", state))
	}
	return fmt.Sprintf("%-7s", state)
}

// titleStyled colors the conventional-commit type prefix of a subject: feat
// green, fix amber, everything procedural dim.
func titleStyled(s string) string {
	i := strings.Index(s, ":")
	if i <= 0 || i > 20 {
		return s
	}
	kind, rest := s[:i], s[i:]
	base := kind
	if j := strings.IndexByte(kind, '('); j > 0 {
		base = kind[:j]
	}
	switch base {
	case "feat":
		return green.Render(kind) + rest
	case "fix", "perf":
		return amber.Render(kind) + rest
	case "docs", "chore", "test", "ci", "refactor", "polish", "style":
		return dim.Render(kind) + rest
	}
	return s
}

// truncPlain shortens a string with no escape sequences in it.
func truncPlain(s string, n int) string {
	r := []rune(s)
	switch {
	case n <= 0:
		return ""
	case len(r) <= n:
		return s
	case n == 1:
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ansiTrunc cuts a rendered string to a printed width, keeping escape
// sequences intact so colors never leak past the cut.
func ansiTrunc(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	printed, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
			b.WriteRune(r)
		case inEscape:
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
		default:
			if printed >= width {
				return b.String() + "\x1b[0m"
			}
			printed++
			b.WriteRune(r)
		}
	}
	return b.String()
}
