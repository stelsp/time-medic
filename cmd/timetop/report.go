// Aggregation: worked minutes become sessions, days, projects — and then a
// daily or weekly you can paste into a standup without editing it.
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Period struct {
	From, To time.Time // [From, To)
	Label    string
}

type Session struct {
	Project    string
	Start, End time.Time
}

type ProjStat struct {
	Name     string
	Mins     int
	Sessions int
	Commits  []Commit
}

type DayStat struct {
	Date    time.Time
	Mins    int
	ByProj  map[string]int
	First   time.Time
	Last    time.Time
	Commits int
	Tokens  Tokens
}

// TaskStat is one unit of work as the forge sees it: a branch, the time that
// went into it, what it produced and whether it landed.
type TaskStat struct {
	Project string
	Branch  string
	Ref     string // issue reference, when the branch or its commits name one
	Mins    int
	Commits []Commit
	Merged  bool
	Gone    bool // the branch is no longer in the repo (merged and deleted)
	Last    time.Time
}

type Report struct {
	Period
	TotalMins int // wall clock: minutes worked at all, overlaps counted once
	AgentMins int // of those, minutes that only an unattended run was awake for
	SumMins   int // sum over projects; larger than TotalMins when work overlaps
	Days      []DayStat
	Projects  []ProjStat
	Sessions  []Session
	Tasks     []TaskStat
	Tokens    map[string]*Tokens // model -> tokens burned in the period
	PrevMins  int                // wall clock of the period before this one
	Gap       int
	// CoverageFrom is the oldest minute any transcript can prove.
	CoverageFrom time.Time
}

// Weekly returns the ISO week containing t, Monday to Monday.
func Weekly(t time.Time) Period {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	off := (int(d.Weekday()) + 6) % 7 // Monday = 0
	from := d.AddDate(0, 0, -off)
	y, w := from.ISOWeek()
	return Period{From: from, To: from.AddDate(0, 0, 7), Label: fmt.Sprintf("%d-W%02d", y, w)}
}

// Daily returns the single day containing t.
func Daily(t time.Time) Period {
	from := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return Period{From: from, To: from.AddDate(0, 0, 1), Label: from.Format("2006-01-02 Mon")}
}

// Build slices the activity down to one period and derives every number the
// views need. Commits are pulled per project from the checkout we saw it in.
func Build(act *Activity, cfg Config, p Period) Report {
	rep := Report{Period: p, Gap: cfg.GapMinutes, CoverageFrom: act.First}
	days := map[string]*DayStat{}
	projMins := map[string]int{}
	projSess := map[string]int{}
	wall := map[minute]bool{}   // union across projects: real elapsed time
	dayWall := map[string]int{} // same, per day

	for proj, set := range act.Minutes {
		var mins []int64
		for m := range set {
			t := time.Unix(int64(m)*60, 0)
			if !t.Before(p.From) && t.Before(p.To) {
				mins = append(mins, int64(m))
			}
		}
		if len(mins) == 0 {
			continue
		}
		sort.Slice(mins, func(i, j int) bool { return mins[i] < mins[j] })
		projMins[proj] = len(mins)
		rep.SumMins += len(mins)

		for _, m := range mins {
			t := time.Unix(m*60, 0)
			key := t.Format("2006-01-02")
			if !wall[minute(m)] {
				wall[minute(m)] = true
				dayWall[key]++
			}
			d := days[key]
			if d == nil {
				d = &DayStat{
					Date:   time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()),
					ByProj: map[string]int{},
					First:  t, Last: t,
				}
				days[key] = d
			}
			d.ByProj[proj]++
			if t.Before(d.First) {
				d.First = t
			}
			if t.After(d.Last) {
				d.Last = t
			}
		}

		// sessions: runs of minutes separated by more than the idle gap
		start, prev := mins[0], mins[0]
		flush := func(a, b int64) {
			rep.Sessions = append(rep.Sessions, Session{
				Project: proj,
				Start:   time.Unix(a*60, 0),
				End:     time.Unix((b+1)*60, 0),
			})
			projSess[proj]++
		}
		for _, m := range mins[1:] {
			if m-prev > int64(cfg.GapMinutes) {
				flush(start, prev)
				start = m
			}
			prev = m
		}
		flush(start, prev)
	}

	rep.TotalMins = len(wall)

	author := cfg.Author
	for proj, mins := range projMins {
		ps := ProjStat{Name: proj, Mins: mins, Sessions: projSess[proj]}
		if root := act.Roots[proj]; root != "" {
			a := author
			if a == "" {
				a = gitAuthor(root)
			}
			ps.Commits = commitsIn(root, a, p.From, p.To)
		}
		rep.Projects = append(rep.Projects, ps)
	}
	// commits land on the day they were authored, tracked or not — a day with
	// commits and no transcript is a gap in coverage, not a day off
	commitsPerDay := map[string]int{}
	for _, ps := range rep.Projects {
		for _, c := range ps.Commits {
			commitsPerDay[c.When.Format("2006-01-02")]++
		}
	}
	for d := p.From; d.Before(p.To); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		ds := days[key]
		if ds == nil {
			ds = &DayStat{Date: d, ByProj: map[string]int{}}
		}
		ds.Mins = dayWall[key]
		ds.Commits = commitsPerDay[key]
		rep.Days = append(rep.Days, *ds)
	}

	sort.Slice(rep.Projects, func(i, j int) bool { return rep.Projects[i].Mins > rep.Projects[j].Mins })
	sort.Slice(rep.Sessions, func(i, j int) bool { return rep.Sessions[i].Start.Before(rep.Sessions[j].Start) })

	for m := range act.Agent {
		t := time.Unix(int64(m)*60, 0)
		if !t.Before(p.From) && t.Before(p.To) && !act.Human[m] {
			rep.AgentMins++
		}
	}

	rep.Tokens = map[string]*Tokens{}
	for i, d := range rep.Days {
		for model, tk := range act.Tokens[d.Date.Format("2006-01-02")] {
			if rep.Tokens[model] == nil {
				rep.Tokens[model] = &Tokens{}
			}
			rep.Tokens[model].add(*tk)
			rep.Days[i].Tokens.add(*tk)
		}
	}
	rep.Tasks = buildTasks(act, cfg, p, author)
	return rep
}

// buildTasks slices the same minutes by branch instead of by project, then
// asks git what each branch produced and whether it landed.
func buildTasks(act *Activity, cfg Config, p Period, author string) []TaskStat {
	var out []TaskStat
	for key, set := range act.Tasks {
		proj, branch := splitTask(key)
		mins, last := 0, time.Time{}
		for m := range set {
			t := time.Unix(int64(m)*60, 0)
			if !t.Before(p.From) && t.Before(p.To) {
				mins++
				if t.After(last) {
					last = t
				}
			}
		}
		if mins == 0 {
			continue
		}
		ts := TaskStat{Project: proj, Branch: branch, Mins: mins, Last: last}
		if root := act.Roots[proj]; root != "" && branch != "" {
			a := author
			if a == "" {
				a = gitAuthor(root)
			}
			ts.Gone = !branchExists(root, branch)
			ts.Commits = commitsOnBranch(root, branch, a, p.From, p.To)
			ts.Merged = branchMerged(root, branch)
		}
		ts.Ref = taskRef(branch, ts.Commits)
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mins > out[j].Mins })
	return out
}

// BuildWeekly is Build for a week, with the week before it measured too so a
// report can say whether the pace went up or down.
func BuildWeekly(act *Activity, cfg Config, anchor time.Time) Report {
	rep := Build(act, cfg, Weekly(anchor))
	prev := Build(act, cfg, Weekly(anchor.AddDate(0, 0, -7)))
	rep.PrevMins = prev.TotalMins
	return rep
}

// Punchcard buckets the last n weeks into weekday × hour so the shape of the
// working day is visible: rows Monday..Sunday, columns 0..23.
func Punchcard(act *Activity, from, to time.Time) [7][24]int {
	var card [7][24]int
	seen := map[minute]bool{}
	for _, set := range act.Minutes {
		for m := range set {
			if seen[m] {
				continue
			}
			seen[m] = true
			t := time.Unix(int64(m)*60, 0)
			if t.Before(from) || !t.Before(to) {
				continue
			}
			card[(int(t.Weekday())+6)%7][t.Hour()]++
		}
	}
	return card
}

func hm(mins int) string {
	if mins <= 0 {
		return "—"
	}
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
}

func bar(mins, max, width int) string {
	if max <= 0 {
		return strings.Repeat("░", width)
	}
	n := mins * width / max
	if n > width {
		n = width
	}
	if n == 0 && mins > 0 {
		n = 1
	}
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

// RenderWeekly is the paste-ready weekly: hours, where they went, what shipped.
// weeklyCommitLimit keeps a weekly readable; --full lifts it.
var weeklyCommitLimit = 10

func RenderWeekly(rep Report, md bool) string {
	var b strings.Builder
	active := 0
	maxDay := 0
	for _, d := range rep.Days {
		if d.Mins > 0 {
			active++
		}
		if d.Mins > maxDay {
			maxDay = d.Mins
		}
	}
	avg := 0
	if active > 0 {
		avg = rep.TotalMins / active
	}
	totalCommits := 0
	for _, p := range rep.Projects {
		totalCommits += len(p.Commits)
	}

	h1, h2 := head(md, 1), head(md, 2)
	fmt.Fprintf(&b, "%sWEEK %s · %s – %s\n\n", h1, rep.Label,
		rep.From.Format("Mon Jan 2"), rep.To.AddDate(0, 0, -1).Format("Mon Jan 2"))
	fmt.Fprintf(&b, "%s · %d active days · %d sessions · avg %s/day · %d commits\n",
		hm(rep.TotalMins), active, len(rep.Sessions), hm(avg), totalCommits)
	if rep.SumMins > rep.TotalMins {
		fmt.Fprintf(&b, "%s across projects (%s of it in parallel sessions)\n",
			hm(rep.SumMins), hm(rep.SumMins-rep.TotalMins))
	}
	if rep.AgentMins > 0 {
		fmt.Fprintf(&b, "%s of it unattended agent runs — no human at the keyboard\n", hm(rep.AgentMins))
	}
	if tokenLine := rep.TokenLine(); tokenLine != "" {
		fmt.Fprintf(&b, "%s\n", tokenLine)
	}
	if d := rep.DeltaLine(); d != "" {
		fmt.Fprintf(&b, "%s\n", d)
	}
	if note := rep.CoverageNote(); note != "" {
		fmt.Fprintf(&b, "%s\n", note)
	}
	fmt.Fprint(&b, "\n")

	fmt.Fprintf(&b, "%sDAYS\n%s", h2, pre(md))
	for _, d := range rep.Days {
		win := ""
		if d.Mins > 0 {
			win = fmt.Sprintf("  %s–%s", d.First.Format("15:04"), d.Last.Format("15:04"))
		}
		top := ""
		if n, m := topProj(d.ByProj); n != "" {
			top = fmt.Sprintf("  %s %s", n, hm(m))
		}
		commits := ""
		if d.Commits > 0 {
			commits = fmt.Sprintf("  %d commits", d.Commits)
		}
		fmt.Fprintf(&b, "%-10s %-14s %8s%s%s%s\n", d.Date.Format("Mon 02"),
			bar(d.Mins, maxDay, 14), hm(d.Mins), win, commits, top)
	}
	fmt.Fprint(&b, post(md)+"\n")

	fmt.Fprintf(&b, "%sPROJECTS\n%s", h2, pre(md))
	for _, p := range rep.Projects {
		pct := 0
		if rep.SumMins > 0 {
			pct = p.Mins * 100 / rep.SumMins
		}
		fmt.Fprintf(&b, "%-22s %-12s %8s  %3d%%  %d sessions, %d commits\n",
			trunc(p.Name, 22), bar(p.Mins, rep.Projects[0].Mins, 12), hm(p.Mins), pct, p.Sessions, len(p.Commits))
	}
	fmt.Fprint(&b, post(md)+"\n")

	fmt.Fprintf(&b, "%sSHIPPED\n", h2)
	for _, p := range rep.Projects {
		if len(p.Commits) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s — %d commits\n%s", p.Name, len(p.Commits), pre(md))
		shown := p.Commits
		if weeklyCommitLimit > 0 && len(shown) > weeklyCommitLimit {
			shown = shown[:weeklyCommitLimit]
		}
		for _, c := range shown {
			fmt.Fprintf(&b, "%s  %s  %s\n", c.When.Format("Mon 02 15:04"), c.SHA, trunc(c.Subject, 78))
		}
		if n := len(p.Commits) - len(shown); n > 0 {
			fmt.Fprintf(&b, "… +%d more (timetop weekly --full)\n", n)
		}
		fmt.Fprint(&b, post(md))
	}
	return b.String()
}

// RenderDaily is the same story for one day, session by session.
func RenderDaily(rep Report, md bool) string {
	var b strings.Builder
	h1, h2 := head(md, 1), head(md, 2)
	fmt.Fprintf(&b, "%sDAY %s\n\n", h1, rep.Label)
	if rep.TotalMins == 0 {
		fmt.Fprint(&b, "no tracked activity\n")
		return b.String()
	}
	d := rep.Days[0]
	fmt.Fprintf(&b, "%s · %s–%s · %d sessions\n\n", hm(rep.TotalMins),
		d.First.Format("15:04"), d.Last.Format("15:04"), len(rep.Sessions))

	fmt.Fprintf(&b, "%sPROJECTS\n%s", h2, pre(md))
	for _, p := range rep.Projects {
		fmt.Fprintf(&b, "%-22s %-12s %8s  %d commits\n", trunc(p.Name, 22),
			bar(p.Mins, rep.Projects[0].Mins, 12), hm(p.Mins), len(p.Commits))
	}
	fmt.Fprint(&b, post(md)+"\n")

	fmt.Fprintf(&b, "%sSESSIONS\n%s", h2, pre(md))
	for _, s := range rep.Sessions {
		fmt.Fprintf(&b, "%s–%s  %8s  %s\n", s.Start.Format("15:04"), s.End.Format("15:04"),
			hm(int(s.End.Sub(s.Start).Minutes())), s.Project)
	}
	fmt.Fprint(&b, post(md)+"\n")

	fmt.Fprintf(&b, "%sSHIPPED\n%s", h2, pre(md))
	n := 0
	for _, p := range rep.Projects {
		for _, c := range p.Commits {
			fmt.Fprintf(&b, "%s  %-16s %s\n", c.When.Format("15:04"), trunc(p.Name, 16), trunc(c.Subject, 70))
			n++
		}
	}
	if n == 0 {
		fmt.Fprint(&b, "no commits\n")
	}
	fmt.Fprint(&b, post(md))
	return b.String()
}

func topProj(m map[string]int) (string, int) {
	best, bestN := "", 0
	for k, v := range m {
		if v > bestN {
			best, bestN = k, v
		}
	}
	return best, bestN
}

func head(md bool, level int) string {
	if !md {
		return ""
	}
	return strings.Repeat("#", level) + " "
}

func pre(md bool) string {
	if md {
		return "```\n"
	}
	return ""
}

func post(md bool) string {
	if md {
		return "```\n"
	}
	return ""
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// CoverageNote warns when the period reaches back past the oldest transcript:
// the missing days are unmeasured, not idle.
func (r Report) CoverageNote() string {
	if r.CoverageFrom.IsZero() || !r.CoverageFrom.After(r.From) {
		return ""
	}
	var untracked []string
	for _, d := range r.Days {
		if d.Mins == 0 && d.Commits > 0 {
			untracked = append(untracked, d.Date.Format("Mon 02"))
		}
	}
	note := "coverage starts " + r.CoverageFrom.Format("Mon 02 Jan 15:04") + " — earlier days are unmeasured"
	if len(untracked) > 0 {
		note += " (commits but no session log: " + strings.Join(untracked, ", ") + ")"
	}
	return note
}

// TokenLine summarises the AI cost of the period in the only unit that needs
// no price list.
func (r Report) TokenLine() string {
	var total Tokens
	models := make([]string, 0, len(r.Tokens))
	for m, tk := range r.Tokens {
		total.add(*tk)
		models = append(models, m)
	}
	if total.Calls == 0 {
		return ""
	}
	sort.Slice(models, func(i, j int) bool {
		return r.Tokens[models[i]].Out > r.Tokens[models[j]].Out
	})
	parts := make([]string, 0, len(models))
	for _, m := range models {
		parts = append(parts, fmt.Sprintf("%s %s out", shortModel(m), compact(r.Tokens[m].Out)))
	}
	return fmt.Sprintf("%d AI calls · %s out · %s in · %s cache read · %s",
		total.Calls, compact(total.Out), compact(total.In+total.CacheW),
		compact(total.CacheR), strings.Join(parts, ", "))
}

// DeltaLine compares the period with the one before it.
func (r Report) DeltaLine() string {
	if r.PrevMins == 0 {
		return ""
	}
	diff := r.TotalMins - r.PrevMins
	sign := "+"
	if diff < 0 {
		sign = "−"
		diff = -diff
	}
	return fmt.Sprintf("%s%s vs the week before (%s)", sign, hm(diff), hm(r.PrevMins))
}

// RenderTasks lists the period's work the way a standup asks for it: by task,
// with its state and what it produced.
func RenderTasks(rep Report, md bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%sTASKS %s · %s – %s\n\n", head(md, 1), rep.Label,
		rep.From.Format("Mon Jan 2"), rep.To.AddDate(0, 0, -1).Format("Mon Jan 2"))
	if len(rep.Tasks) == 0 {
		fmt.Fprint(&b, "no tracked work\n")
		return b.String()
	}
	top := rep.Tasks[0].Mins
	fmt.Fprint(&b, pre(md))
	for _, t := range rep.Tasks {
		fmt.Fprintf(&b, "%-24s %-10s %8s  %-7s %3d commits  %s\n",
			trunc(t.Label(), 24), bar(t.Mins, top, 10), hm(t.Mins), t.State(),
			len(t.Commits), trunc(t.Project, 18))
	}
	fmt.Fprint(&b, post(md)+"\n")

	for _, t := range rep.Tasks {
		if len(t.Commits) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s%s — %s, %d commits\n%s", head(md, 2), t.Label(), t.State(), len(t.Commits), pre(md))
		shown := t.Commits
		if weeklyCommitLimit > 0 && len(shown) > weeklyCommitLimit {
			shown = shown[:weeklyCommitLimit]
		}
		for _, c := range shown {
			fmt.Fprintf(&b, "%s  %s  %s\n", c.When.Format("Mon 02 15:04"), c.SHA, trunc(c.Subject, 74))
		}
		if n := len(t.Commits) - len(shown); n > 0 {
			fmt.Fprintf(&b, "… +%d more (--full)\n", n)
		}
		fmt.Fprint(&b, post(md)+"\n")
	}
	return b.String()
}

// Label is the task as a human names it: the issue reference when there is
// one, otherwise the branch.
func (t TaskStat) Label() string {
	switch {
	case t.Branch == "":
		return "(no branch)"
	case t.Ref != "" && !strings.Contains(t.Branch, strings.TrimPrefix(t.Ref, "#")):
		return t.Branch + " " + t.Ref
	default:
		return t.Branch
	}
}

// State is where the task stands on the integration branch.
func (t TaskStat) State() string {
	switch {
	case t.Branch == "":
		return "—"
	case isTrunk(t.Branch):
		return "trunk"
	case t.Gone:
		return "gone"
	case t.Merged:
		return "merged"
	case len(t.Commits) > 0:
		return "open"
	default:
		return "quiet"
	}
}

func shortModel(m string) string {
	m = strings.TrimPrefix(m, "claude-")
	if i := strings.Index(m, "-2"); i > 0 { // strip date suffixes like -20251001
		m = m[:i]
	}
	return m
}

// compact renders big counts as 1.2M / 340k so a header line stays a line.
func compact(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}
