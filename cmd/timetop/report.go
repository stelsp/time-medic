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
}

type Report struct {
	Period
	TotalMins int // wall clock: minutes worked at all, overlaps counted once
	SumMins   int // sum over projects; larger than TotalMins when work overlaps
	Days      []DayStat
	Projects  []ProjStat
	Sessions  []Session
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
	return rep
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
