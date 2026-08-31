// The dashboard: three screens over the same minutes — the week, the tasks
// those minutes went into, and the rhythm they form over time.
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
	focusDay
)

const (
	screenWeek = iota + 1
	screenTasks
	screenRhythm
)

type model struct {
	cfg       Config
	act       *Activity
	rep       Report
	anchor    time.Time // any day inside the shown week
	screen    int
	sel       int // selected day index
	selProj   int
	selTask   int
	taskFocus bool // false: the task list, true: its commit panel
	dayScrol  int
	taskScr   int
	focus     focusArea
	dayRep    Report // the selected day, rebuilt on selection — not per frame
	dayKey    string
	card      [7][24]int // the rhythm sweep, cached with the scan behind it
	cardKey   string
	scanSeq   int // bumped by every scan, so caches keyed on it expire
	w, h      int
	loading   bool
	note      string
	noteTill  time.Time
}

type scanned struct{ act *Activity }
type tick time.Time

func newModel(cfg Config) model {
	return model{cfg: cfg, anchor: time.Now(), screen: screenWeek,
		sel: int(time.Now().Weekday()+6) % 7, loading: true}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.rescan(), tickEvery()) }

func (m model) rescan() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		act, err := Scan(cfg)
		if err != nil {
			act = &Activity{
				Minutes: map[string]map[minute]bool{}, Tasks: map[string]map[minute]bool{},
				Agent: map[minute]bool{}, Human: map[minute]bool{},
				Roots: map[string]string{}, Tokens: map[string]map[string]*Tokens{},
			}
		}
		return scanned{act}
	}
}

func tickEvery() tea.Cmd {
	return tea.Tick(60*time.Second, func(t time.Time) tea.Msg { return tick(t) })
}

func (m *model) reload() {
	if m.act == nil {
		return
	}
	m.rep = BuildWeekly(m.act, m.cfg, m.anchor)
	m.dayKey = "" // the selected day's report is now stale
	m.loadDay()
}

// loadDay rebuilds the selected day only when the selection actually moved:
// Build shells out to git, and doing that on every frame stutters the UI.
func (m *model) loadDay() {
	if m.act == nil || m.sel >= len(m.rep.Days) {
		return
	}
	key := m.rep.Days[m.sel].Date.Format("2006-01-02")
	if key == m.dayKey {
		return
	}
	m.dayRep = Build(m.act, m.cfg, Daily(m.rep.Days[m.sel].Date))
	m.dayKey = key
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case scanned:
		m.act = msg.act
		m.scanSeq++
		gitForget() // a fresh scan may have new commits behind it
		m.reload()
		m.loading = false
	case tick:
		return m, tea.Batch(m.rescan(), tickEvery())
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// the first scan has not landed yet: only quitting and rescanning are safe
	if m.act == nil {
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			return m, m.rescan()
		}
		return m, nil
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.screen != screenWeek {
			m.screen = screenWeek
			return m, nil
		}
		return m, tea.Quit
	case "1":
		m.screen = screenWeek
	case "2":
		m.screen = screenTasks
	case "3":
		m.screen = screenRhythm
	case "tab":
		if m.screen == screenWeek {
			m.focus = (m.focus + 1) % 3
		} else if m.screen == screenTasks {
			m.taskFocus = !m.taskFocus // list ↔ the commits it produced
		}
	case "left", "h":
		m.anchor = m.anchor.AddDate(0, 0, -7)
		m.resetScroll()
		m.reload()
	case "right", "l":
		if !Weekly(m.anchor).To.After(time.Now()) {
			m.anchor = m.anchor.AddDate(0, 0, 7)
			m.resetScroll()
			m.reload()
		}
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-10)
	case "pgdown":
		m.move(10)
	case "t":
		m.anchor = time.Now()
		m.sel = int(time.Now().Weekday()+6) % 7
		m.resetScroll()
		m.reload()
	case "r":
		m.loading = true
		return m, m.rescan()
	case "y":
		m.flash(copyResult("weekly", copyClip(RenderWeekly(m.rep, true))))
	case "Y":
		m.loadDay()
		m.flash(copyResult("daily", copyClip(RenderDaily(m.dayRep, true))))
	case "T":
		m.flash(copyResult("task breakdown", copyClip(RenderTasks(m.rep, true))))
	}
	return m, nil
}

func (m *model) resetScroll() { m.dayScrol, m.taskScr, m.selTask = 0, 0, 0 }

func (m *model) flash(s string) { m.note, m.noteTill = s, time.Now().Add(3*time.Second) }

// move applies ↑↓ to whatever the screen and focus point at.
func (m *model) move(d int) {
	switch m.screen {
	case screenTasks:
		if m.taskFocus {
			m.taskScr = clamp(m.taskScr+d, 0, m.taskLines())
			return
		}
		m.selTask = clamp(m.selTask+d, 0, len(m.rep.Tasks)-1)
		m.taskScr = 0
		return
	case screenRhythm:
		return // nothing to move through: the whole card is one picture
	}
	switch m.focus {
	case focusDays:
		m.sel = clamp(m.sel+d, 0, len(m.rep.Days)-1)
		m.dayScrol = 0
		m.loadDay()
	case focusProjects:
		m.selProj = clamp(m.selProj+d, 0, len(m.rep.Projects)-1)
	case focusDay:
		m.dayScrol = clamp(m.dayScrol+d, 0, m.dayLines())
	}
}

// dayLines and taskLines bound the scroll offsets by what actually exists, so
// overshooting the end does not cost hundreds of dead keypresses.
func (m model) dayLines() int {
	n := len(m.dayRep.Sessions) + len(m.dayRep.Tasks)
	for _, p := range m.dayRep.Projects {
		n += len(p.Commits)
	}
	return n
}

func (m model) taskLines() int {
	if m.selTask >= len(m.rep.Tasks) {
		return 0
	}
	return len(m.rep.Tasks[m.selTask].Commits)
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return max(lo, min(v, hi))
}

func (m model) View() string {
	if m.w == 0 {
		return "…"
	}
	if m.loading && m.act == nil {
		return "\n  " + amber.Render("✦") + " time-medic — reading the trail…\n"
	}
	var body string
	switch m.screen {
	case screenTasks:
		body = m.tasksScreen()
	case screenRhythm:
		mm := m
		body = mm.rhythmScreen()
	default:
		body = m.weekScreen()
	}
	return fitFrame(m.header()+"\n"+body+"\n"+m.footer(), m.w, m.h)
}

// header carries identity, the week's totals and — when work is happening
// right now — a live marker instead of a dead clock.
func (m model) header() string {
	tabs := []string{"1 WEEK", "2 TASKS", "3 RHYTHM"}
	var tb strings.Builder
	for i, t := range tabs {
		if i+1 == m.screen {
			tb.WriteString(amberB.Render(" " + t + " "))
		} else {
			tb.WriteString(dim.Render(" " + t + " "))
		}
	}
	spend := ""
	if m.rep.CostTotal > 0 {
		spend = "  " + green.Render(money(m.rep.CostTotal))
	}
	left := amberB.Render(" ✦ time-medic ") + dim.Render(m.rep.Label+"  ") +
		bold.Render(hm(m.rep.TotalMins)) + " " + gauge(m.rep.TotalMins, m.cfg.TargetHours, 12) +
		dim.Render(fmt.Sprintf(" of %.0fh", m.cfg.TargetHours)) + spend + "  " + tb.String()
	right := m.nowMark()
	pad := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if pad < 1 {
		// a narrow terminal keeps the identity and the totals, drops the rest
		if lipgloss.Width(left) >= m.w {
			return ansiTrunc(left, m.w)
		}
		return left
	}
	return left + strings.Repeat(" ", pad) + right
}

// nowMark shows the session in progress: a pulse, the project, and how long
// the current stretch has been running.
func (m model) nowMark() string {
	proj, mins := m.currentSession()
	if proj == "" {
		return dim.Render(time.Now().Format("Mon 02 Jan 15:04"))
	}
	return green.Render("● ") + bold.Render(proj) + " " + amber.Render(hm(mins)) +
		dim.Render("  "+time.Now().Format("15:04"))
}

// currentSession is the session touching the present minute, if any.
func (m model) currentSession() (string, int) {
	if m.act == nil {
		return "", 0
	}
	now := time.Now()
	today := Build(m.act, m.cfg, Daily(now))
	for i := len(today.Sessions) - 1; i >= 0; i-- {
		s := today.Sessions[i]
		if now.Sub(s.End) <= time.Duration(m.cfg.GapMinutes)*time.Minute {
			return s.Project, int(now.Sub(s.Start).Minutes())
		}
	}
	return "", 0
}

func (m model) footer() string {
	if m.note != "" && time.Now().Before(m.noteTill) {
		return amber.Render(" " + m.note)
	}
	switch m.screen {
	case screenTasks:
		return dim.Render(" ↑↓ task · ←→ week · T copy tasks · 1 week · 3 rhythm · r rescan · q quit")
	case screenRhythm:
		return dim.Render(" ←→ week · 1 week · 2 tasks · r rescan · q quit")
	}
	return dim.Render(" ↑↓ move · tab panel · ←→ week · t today · y weekly · Y daily · 2 tasks · 3 rhythm · q quit")
}

// ── screen 1: the week ──────────────────────────────────────────────────────

func (m model) weekScreen() string {
	leftW := m.w * 3 / 5
	rightW := m.w - leftW
	// the seven day rows want nine lines; a short terminal gets fewer and the
	// day panel below keeps at least three
	topH := clamp(m.h-7, 3, 9)
	days := titledBox(leftW, "DAYS", weekMeta(m.rep), m.daysBody(leftW-4), topH, m.focus == focusDays)
	projects := titledBox(rightW, "PROJECTS", fmt.Sprintf("%d", len(m.rep.Projects)),
		m.projectsBody(rightW-4), topH, m.focus == focusProjects)
	top := lipgloss.JoinHorizontal(lipgloss.Top, days, projects)

	detailH := m.h - lipgloss.Height(top) - 4
	if detailH < 3 {
		detailH = 3
	}
	d := m.rep.Days[m.sel]
	meta := hm(d.Mins)
	if d.Tokens.Calls > 0 {
		meta += fmt.Sprintf(" · %d calls · %s out", d.Tokens.Calls, compact(d.Tokens.Out))
	}
	if d.Cost > 0 {
		meta += " · " + money(d.Cost)
	}
	detail := titledBox(m.w, "DAY "+d.Date.Format("Mon 02 Jan"), meta,
		m.dayBody(detailH-1), detailH, m.focus == focusDay)
	return top + "\n" + detail
}

func weekMeta(rep Report) string {
	active := 0
	for _, d := range rep.Days {
		if d.Mins > 0 {
			active++
		}
	}
	meta := fmt.Sprintf("%d/7 days · %d sessions", active, len(rep.Sessions))
	if d := rep.DeltaLine(); d != "" {
		meta += " · " + strings.SplitN(d, " vs", 2)[0] + " vs last"
	}
	return meta
}

func (m model) daysBody(w int) string {
	// a short box shows the week around the cursor rather than clipping it
	maxDay := 0
	for _, d := range m.rep.Days {
		if d.Mins > maxDay {
			maxDay = d.Mins
		}
	}
	// columns give way in order as the terminal narrows: the time window
	// first, then the bar, so the day and its hours always survive
	// budget: cursor mark, today dot, day name, bar, hours, time window
	const nameW, hoursW, winW = 7, 8, 11
	barW := w - 4 - nameW - hoursW - winW - 3
	showWin := barW >= 6
	if !showWin {
		barW = w - 4 - nameW - hoursW - 1
	}
	showBar := barW >= 3

	var b strings.Builder
	today := time.Now().Format("2006-01-02")
	for _, d := range m.rep.Days {
		mark := " "
		if d.Date.Format("2006-01-02") == today {
			mark = amber.Render("•")
		}
		row := fmt.Sprintf("%s %-*s", mark, nameW, d.Date.Format("Mon 02"))

		if showBar {
			row += " " + barC(d.Mins, maxDay, barW)
		}
		row += fmt.Sprintf(" %*s", hoursW, hm(d.Mins))
		if showWin {
			win := "     —     "
			if d.Mins > 0 {
				win = d.First.Format("15:04") + "–" + d.Last.Format("15:04")
			}
			row += "  " + dim.Render(win)
		}
		b.WriteString(row + "\n")
	}
	rows := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for i := range rows {
		if i == m.sel {
			rows[i] = amberB.Render("▎") + rows[i] // the cursor occupies its own cell
		} else {
			rows[i] = " " + rows[i]
		}
	}
	visible := clamp(m.h-9, 1, len(rows))
	return window(rows, scrollFor(m.sel, visible, len(rows)), visible)
}

func (m model) projectsBody(w int) string {
	if len(m.rep.Projects) == 0 {
		return dim.Render("no activity this week")
	}
	const hoursW = 8
	barW := clamp(w-hoursW-14, 0, 8)
	nameW := w - hoursW - barW - 3
	if nameW < 4 {
		nameW = max(w-hoursW-2, 4)
		barW = 0
	}
	rows := clamp(m.h-9, 1, len(m.rep.Projects))
	var b strings.Builder
	top := m.rep.Projects[0].Mins
	for i, p := range m.rep.Projects {
		if i >= rows {
			b.WriteString(dim.Render(fmt.Sprintf("  … +%d more", len(m.rep.Projects)-i)))
			break
		}
		row := fmt.Sprintf("%-*s", nameW, trunc(p.Name, nameW))
		if barW > 0 {
			row += " " + barC(p.Mins, top, barW)
		}
		row += fmt.Sprintf(" %*s", hoursW, hm(p.Mins))
		if i == m.selProj && m.focus == focusProjects {
			row = amberB.Render("▎") + row
		} else {
			row = " " + row
		}
		b.WriteString(row + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// dayBody is the selected day up close: its sessions and what they shipped,
// scrollable because a good day ships more than fits.
func (m model) dayBody(rows int) string {
	d := m.rep.Days[m.sel]
	if d.Mins == 0 {
		return dim.Render("nothing tracked")
	}
	day := m.dayRep
	var lines []string
	for _, s := range day.Sessions {
		lines = append(lines, fmt.Sprintf(" %s–%s %8s  %s",
			s.Start.Format("15:04"), s.End.Format("15:04"),
			hm(int(s.End.Sub(s.Start).Minutes())), s.Project))
	}
	if len(day.Tasks) > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf(" ── tasks %d ──", len(day.Tasks))))
		top := day.Tasks[0].Mins
		for _, ts := range day.Tasks {
			lines = append(lines, fmt.Sprintf(" %-24s %s %8s  %s %s",
				trunc(ts.Label(), 24), barC(ts.Mins, top, 10), hm(ts.Mins),
				stateStyled(ts.State()), dim.Render(fmt.Sprintf("%d commits", len(ts.Commits)))))
		}
	}
	var commits []string
	for _, p := range day.Projects {
		for _, c := range p.Commits {
			commits = append(commits, fmt.Sprintf(" %s %s %s", dim.Render(c.When.Format("15:04")),
				green.Render("✓"), titleStyled(trunc(c.Subject, m.w-14))))
		}
	}
	if len(commits) > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf(" ── shipped %d ──", len(commits))))
		lines = append(lines, commits...)
	}
	return window(lines, m.dayScrol, rows)
}

// ── screen 2: tasks ─────────────────────────────────────────────────────────

func (m model) tasksScreen() string {
	listH := clamp(len(m.rep.Tasks), 4, max(4, m.h/2-2))
	listW := m.w
	var rows []string
	top := 1
	if len(m.rep.Tasks) > 0 {
		top = m.rep.Tasks[0].Mins
	}
	labelW := clamp(m.w/4, 12, 30)
	for i, t := range m.rep.Tasks {
		row := fmt.Sprintf("%-*s %-16s %s %8s  %-7s %s",
			labelW, trunc(t.Label(), labelW), trunc(t.Project, 16),
			barC(t.Mins, top, 10), hm(t.Mins), stateStyled(t.State()),
			dim.Render(fmt.Sprintf("%d commits", len(t.Commits))))
		if i == m.selTask {
			row = amberB.Render("▎") + row
		} else {
			row = " " + row
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = []string{dim.Render(" no tracked work this week")}
	}
	body := window(rows, scrollFor(m.selTask, listH, len(rows)), listH)
	list := titledBox(listW, "TASKS", fmt.Sprintf("%d · %s", len(m.rep.Tasks), hm(m.rep.TotalMins)),
		body, listH, !m.taskFocus)

	detailH := m.h - lipgloss.Height(list) - 4
	if detailH < 3 {
		detailH = 3
	}
	return list + "\n" + titledBox(m.w, "WHAT IT PRODUCED", m.taskMeta(), m.taskBody(detailH-1), detailH, m.taskFocus)
}

func (m model) taskMeta() string {
	if m.selTask >= len(m.rep.Tasks) {
		return ""
	}
	t := m.rep.Tasks[m.selTask]
	return t.Label() + " · " + t.State()
}

func (m model) taskBody(rows int) string {
	if m.selTask >= len(m.rep.Tasks) {
		return dim.Render("nothing selected")
	}
	t := m.rep.Tasks[m.selTask]
	if len(t.Commits) == 0 {
		return dim.Render("no commits in this period — time spent reading, planning or reviewing")
	}
	var lines []string
	for _, c := range t.Commits {
		lines = append(lines, fmt.Sprintf(" %s %s %s", dim.Render(c.When.Format("Mon 02 15:04")),
			dim.Render(c.SHA), titleStyled(trunc(c.Subject, m.w-24))))
	}
	return window(lines, m.taskScr, rows)
}

// ── screen 3: rhythm ────────────────────────────────────────────────────────

// rhythmScreen answers a question a weekly cannot: when do you actually work.
func (m *model) rhythmScreen() string {
	weeks := 8
	to := Weekly(m.anchor).To
	from := to.AddDate(0, 0, -7*weeks)
	card := m.punchcard(from, to)

	peak := 0
	for _, row := range card {
		for _, v := range row {
			peak = max(peak, v)
		}
	}
	var b strings.Builder
	ruler := strings.Repeat(" ", 5)
	for h := 0; h < 24; h += 3 {
		ruler += fmt.Sprintf("%02d ", h)
	}
	b.WriteString(dim.Render(ruler) + "\n")
	names := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	dayTotal := [7]int{}
	for d, row := range card {
		b.WriteString(" " + dim.Render(names[d]) + " ")
		for h := 0; h < 24; h++ {
			b.WriteString(heat(row[h], peak))
			dayTotal[d] += row[h]
		}
		b.WriteString(fmt.Sprintf("  %8s\n", hm(dayTotal[d])))
	}
	hours := [24]int{}
	total := 0
	for _, row := range card {
		for h, v := range row {
			hours[h] += v
			total += v
		}
	}
	b.WriteString("\n" + dim.Render(" hour of day") + "\n ")
	peakHour, peakHourN := 0, 0
	for h := 0; h < 24; h++ {
		if hours[h] > peakHourN {
			peakHour, peakHourN = h, hours[h]
		}
	}
	for h := 0; h < 24; h++ {
		b.WriteString(vbar(hours[h], peakHourN))
	}
	b.WriteString("\n\n")
	if total == 0 {
		return titledBox(m.w, "RHYTHM", "", dim.Render("no tracked minutes in these eight weeks"),
			m.h-4, false)
	}
	activeWeeks := activeWeekCount(m.act, from, to)
	perWeek := 0
	if activeWeeks > 0 {
		perWeek = total / activeWeeks
	}
	fmt.Fprintf(&b, " %s over %d active weeks · busiest %s %s · peak hour %02d:00 · %s/active week\n",
		bold.Render(hm(total)), activeWeeks, names[argmax(dayTotal[:])],
		hm(dayTotal[argmax(dayTotal[:])]), peakHour, hm(perWeek))
	if m.act != nil && len(m.act.Tokens) > 0 {
		fmt.Fprintf(&b, " %s\n", m.rep.TokenLine())
	}
	if spend := m.rep.SpendLine(); spend != "" {
		fmt.Fprintf(&b, " %s\n", spend)
	}
	h := m.h - 4
	return titledBox(m.w, "RHYTHM", fmt.Sprintf("%s – %s", from.Format("Jan 2"), to.AddDate(0, 0, -1).Format("Jan 2")),
		b.String(), h, false)
}

// punchcard caches the eight-week sweep: it walks every tracked minute, and
// the rhythm screen would otherwise redo that on every frame.
func (m *model) punchcard(from, to time.Time) [7][24]int {
	key := fmt.Sprintf("%s|%s|%d", from.Format("2006-01-02"), to.Format("2006-01-02"), m.scanSeq)
	if key == m.cardKey {
		return m.card
	}
	m.card, m.cardKey = Punchcard(m.act, from, to), key
	return m.card
}

func argmax(xs []int) int {
	best := 0
	for i, v := range xs {
		if v > xs[best] {
			best = i
		}
	}
	return best
}

// ── helpers ─────────────────────────────────────────────────────────────────

// fitFrame is the last guarantee that a frame fits its terminal: every line
// cut to the width, the frame cut to the height. Panels budget their own space
// first; this is the backstop that keeps a small window from scrolling.
func fitFrame(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if h > 0 && len(lines) > h {
		lines = lines[:h]
	}
	for i, ln := range lines {
		if lipgloss.Width(ln) > w {
			lines[i] = ansiTrunc(ln, w)
		}
	}
	return strings.Join(lines, "\n")
}

// window shows `rows` lines starting at offset, with markers for what is cut.
func window(lines []string, offset, rows int) string {
	if rows <= 0 || len(lines) == 0 {
		return ""
	}
	if len(lines) <= rows {
		return strings.Join(lines, "\n")
	}
	offset = clamp(offset, 0, len(lines)-rows)
	out := append([]string{}, lines[offset:offset+rows]...)
	// with only a row or two there is no room to spend on markers — content wins
	if rows > 2 {
		if offset > 0 {
			out[0] = dim.Render(fmt.Sprintf(" ↑ %d more above", offset))
		}
		if offset+rows < len(lines) {
			out[len(out)-1] = dim.Render(fmt.Sprintf(" ↓ %d more below", len(lines)-offset-rows))
		}
	}
	return strings.Join(out, "\n")
}

// scrollFor keeps the cursor inside the visible window.
func scrollFor(sel, rows, total int) int {
	if rows <= 0 || total <= rows {
		return 0
	}
	return clamp(sel-rows/2, 0, total-rows)
}

// renderOnce paints one frame to stdout — no alt screen, no input. COLUMNS,
// LINES and TIMETOP_SCREEN make a smoke test reproducible.
func renderOnce(cfg Config, act *Activity) string {
	m := newModel(cfg)
	m.act = act
	m.reload()
	m.loading = false
	m.w, m.h = envInt("COLUMNS", 120), envInt("LINES", 40)
	m.screen = envInt("TIMETOP_SCREEN", screenWeek)
	return m.View() + "\n"
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

// copyResult words what actually happened, so a failed copy never reads as a
// success the user then pastes nothing from.
func copyResult(what string, err error) string {
	if err != nil {
		return what + " not copied: " + err.Error()
	}
	return what + " copied as markdown"
}

// copyClip pushes text to whatever clipboard this machine has.
func copyClip(s string) error {
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
		return fmt.Errorf("no clipboard tool found (wl-copy, xclip or xsel)")
	}
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// activeWeekCount counts the weeks that actually hold work, so an average is
// not diluted by weeks the transcripts never covered.
func activeWeekCount(act *Activity, from, to time.Time) int {
	if act == nil {
		return 0
	}
	weeks := map[string]bool{}
	for _, set := range act.Minutes {
		for m := range set {
			t := time.Unix(int64(m)*60, 0)
			if t.Before(from) || !t.Before(to) {
				continue
			}
			y, w := t.ISOWeek()
			weeks[fmt.Sprintf("%d-%d", y, w)] = true
		}
	}
	return len(weeks)
}
