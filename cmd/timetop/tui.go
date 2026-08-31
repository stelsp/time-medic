// The dashboard: this week at a glance, the day under the cursor in detail.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type focusArea int

const (
	focusDays focusArea = iota
	focusProjects
)

type model struct {
	cfg      Config
	act      *Activity
	rep      Report
	anchor   time.Time // any day inside the shown week
	sel      int       // selected day index
	selProj  int
	focus    focusArea
	w, h     int
	loading  bool
	note     string
	noteTill time.Time
}

type scanned struct{ act *Activity }
type tick time.Time

func newModel(cfg Config) model {
	return model{cfg: cfg, anchor: time.Now(), sel: int(time.Now().Weekday()+6) % 7, loading: true}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.rescan(), tickEvery())
}

func (m model) rescan() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		act, err := Scan(cfg)
		if err != nil {
			act = &Activity{Minutes: map[string]map[minute]bool{}, Roots: map[string]string{}}
		}
		return scanned{act}
	}
}

func tickEvery() tea.Cmd {
	return tea.Tick(60*time.Second, func(t time.Time) tea.Msg { return tick(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case scanned:
		m.act = msg.act
		m.rep = Build(m.act, m.cfg, Weekly(m.anchor))
		m.loading = false
	case tick:
		return m, tea.Batch(m.rescan(), tickEvery())
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "tab":
		m.focus = (m.focus + 1) % 2
	case "left", "h":
		m.anchor = m.anchor.AddDate(0, 0, -7)
		m.rep = Build(m.act, m.cfg, Weekly(m.anchor))
	case "right", "l":
		if !Weekly(m.anchor).To.After(time.Now()) {
			m.anchor = m.anchor.AddDate(0, 0, 7)
			m.rep = Build(m.act, m.cfg, Weekly(m.anchor))
		}
	case "up", "k":
		if m.focus == focusDays && m.sel > 0 {
			m.sel--
		} else if m.focus == focusProjects && m.selProj > 0 {
			m.selProj--
		}
	case "down", "j":
		if m.focus == focusDays && m.sel < 6 {
			m.sel++
		} else if m.focus == focusProjects && m.selProj < len(m.rep.Projects)-1 {
			m.selProj++
		}
	case "t":
		m.anchor = time.Now()
		m.sel = int(time.Now().Weekday()+6) % 7
		m.rep = Build(m.act, m.cfg, Weekly(m.anchor))
	case "r":
		m.loading = true
		return m, m.rescan()
	case "y":
		copyClip(RenderWeekly(m.rep, true))
		m.note, m.noteTill = "weekly copied as markdown", time.Now().Add(3*time.Second)
	case "Y":
		day := Build(m.act, m.cfg, Daily(m.rep.Days[m.sel].Date))
		copyClip(RenderDaily(day, true))
		m.note, m.noteTill = "daily copied as markdown", time.Now().Add(3*time.Second)
	}
	return m, nil
}

func (m model) View() string {
	if m.w == 0 {
		return "…"
	}
	if m.loading && m.act == nil {
		return "\n  " + amber.Render("✦") + " time-medic — reading the trail…\n"
	}
	w := m.w
	head := amberB.Render(" ✦ time-medic ") + dim.Render(m.rep.Label+"  ") +
		bold.Render(hm(m.rep.TotalMins)) + " " + gauge(m.rep.TotalMins, m.cfg.TargetHours, 12) +
		dim.Render(fmt.Sprintf(" of %.0fh", m.cfg.TargetHours))
	right := dim.Render(time.Now().Format("Mon 02 Jan 15:04"))
	pad := w - lipgloss.Width(head) - lipgloss.Width(right) - 1
	if pad < 1 {
		pad = 1
	}
	header := head + strings.Repeat(" ", pad) + right

	leftW := w * 3 / 5
	rightW := w - leftW
	days := titledBox(leftW, "DAYS", weekMeta(m.rep), m.daysBody(leftW-4), 9, m.focus == focusDays)
	projects := titledBox(rightW, "PROJECTS", fmt.Sprintf("%d", len(m.rep.Projects)),
		m.projectsBody(rightW-4), 9, m.focus == focusProjects)
	top := lipgloss.JoinHorizontal(lipgloss.Top, days, projects)

	detailH := m.h - lipgloss.Height(top) - 4
	if detailH < 3 {
		detailH = 3
	}
	d := m.rep.Days[m.sel]
	detail := titledBox(w, "DAY "+d.Date.Format("Mon 02 Jan"), hm(d.Mins), m.dayBody(detailH-1), detailH, false)

	foot := dim.Render(" ↑↓ day · tab panel · ←→ week · t today · y weekly · Y daily · r rescan · q quit")
	if m.note != "" && time.Now().Before(m.noteTill) {
		foot = amber.Render(" " + m.note)
	}
	return header + "\n" + top + "\n" + detail + "\n" + foot
}

func weekMeta(rep Report) string {
	active := 0
	for _, d := range rep.Days {
		if d.Mins > 0 {
			active++
		}
	}
	return fmt.Sprintf("%d/7 days · %d sessions", active, len(rep.Sessions))
}

func (m model) daysBody(w int) string {
	maxDay := 0
	for _, d := range m.rep.Days {
		if d.Mins > maxDay {
			maxDay = d.Mins
		}
	}
	barW := w - 33 // row chrome: cursor, name, hours and the time window
	if barW < 6 {
		barW = 6
	}
	var b strings.Builder
	today := time.Now().Format("2006-01-02")
	for i, d := range m.rep.Days {
		mark := " "
		if d.Date.Format("2006-01-02") == today {
			mark = amber.Render("•")
		}
		win := "     —     "
		if d.Mins > 0 {
			win = d.First.Format("15:04") + "–" + d.Last.Format("15:04")
		}
		row := fmt.Sprintf("%s %-7s %s %8s  %s", mark, d.Date.Format("Mon 02"),
			bar(d.Mins, maxDay, barW), hm(d.Mins), dim.Render(win))
		if i == m.sel {
			row = selMark(row)
		} else {
			row = " " + row
		}
		b.WriteString(row + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) projectsBody(w int) string {
	if len(m.rep.Projects) == 0 {
		return dim.Render("no activity this week")
	}
	var b strings.Builder
	top := m.rep.Projects[0].Mins
	nameW := w - 26
	if nameW < 8 {
		nameW = 8
	}
	for i, p := range m.rep.Projects {
		if i > 6 {
			b.WriteString(dim.Render(fmt.Sprintf("  … +%d more", len(m.rep.Projects)-i)))
			break
		}
		row := fmt.Sprintf(" %-*s %s %8s", nameW, trunc(p.Name, nameW), bar(p.Mins, top, 8), hm(p.Mins))
		if i == m.selProj && m.focus == focusProjects {
			row = selMark(row)
		}
		b.WriteString(row + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// dayBody is the selected day up close: its sessions and what they shipped.
func (m model) dayBody(rows int) string {
	d := m.rep.Days[m.sel]
	if d.Mins == 0 {
		return dim.Render("nothing tracked")
	}
	day := Build(m.act, m.cfg, Daily(d.Date))
	var lines []string
	for _, s := range day.Sessions {
		lines = append(lines, fmt.Sprintf(" %s–%s %8s  %s",
			s.Start.Format("15:04"), s.End.Format("15:04"),
			hm(int(s.End.Sub(s.Start).Minutes())), s.Project))
	}
	var commits []string
	for _, p := range day.Projects {
		for _, c := range p.Commits {
			commits = append(commits, fmt.Sprintf(" %s %s %s", dim.Render(c.When.Format("15:04")),
				green.Render("✓"), trunc(c.Subject, m.w-14)))
		}
	}
	if len(commits) > 0 {
		lines = append(lines, dim.Render(" ── shipped ──"))
		lines = append(lines, commits...)
	}
	if len(lines) > rows {
		hidden := len(lines) - rows + 1
		lines = append(lines[:rows-1], dim.Render(fmt.Sprintf(" … +%d more", hidden)))
	}
	return strings.Join(lines, "\n")
}

// copyClip pushes text to whatever clipboard this machine has.
func copyClip(s string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-ib"}} {
			if _, err := exec.LookPath(c[0]); err == nil {
				cmd = exec.Command(c[0], c[1:]...)
				break
			}
		}
	}
	if cmd == nil {
		return
	}
	cmd.Stdin = strings.NewReader(s)
	_ = cmd.Run()
}

// renderOnce paints one dashboard frame to stdout — no alt screen, no input.
// COLUMNS/LINES set the canvas so a smoke test is reproducible.
func renderOnce(cfg Config, act *Activity) string {
	m := newModel(cfg)
	m.act = act
	m.rep = Build(act, cfg, Weekly(m.anchor))
	m.loading = false
	m.w, m.h = envInt("COLUMNS", 120), envInt("LINES", 40)
	return m.View() + "\n"
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}
