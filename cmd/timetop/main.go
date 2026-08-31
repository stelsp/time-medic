// timetop — autonomous time tracking. No timers to start: the work leaves a
// trail, this reads it. `timetop` opens the dashboard, `timetop weekly` and
// `timetop daily` print a report.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `timetop — autonomous time tracking from your work trail

  timetop                    dashboard (TUI)
  timetop daily [when]       when: today (default) | yesterday | YYYY-MM-DD
  timetop weekly [when]      when: this (default) | last | YYYY-MM-DD | -N weeks
  timetop tasks [when]       one line per branch: time, state, commits
  timetop scan               refresh the cache and print what was found

  --md                       markdown output, ready to paste
  --full                     every commit, not just the first ten per project
  --once                     render the dashboard once to stdout (smoke test)
  --out PATH                 write the report to a file instead of stdout
  --slack                    post the report to SLACK_WEBHOOK from the config
`

func main() {
	cfg := LoadConfig()
	args := os.Args[1:]
	md, once, slack := false, false, false
	out := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if v, ok := strings.CutPrefix(a, "--out="); ok {
			out = v
			continue
		}
		if a == "--out" && i+1 < len(args) {
			out = args[i+1]
			i++
			continue
		}
		switch a {
		case "--md":
			md = true
		case "--full":
			weeklyCommitLimit = 0
		case "--once":
			once = true
		case "--slack":
			slack = true
		case "-h", "--help", "help":
			fmt.Print(usage)
			return
		default:
			rest = append(rest, a)
		}
	}

	cmd := ""
	if len(rest) > 0 {
		cmd = rest[0]
	}
	when := ""
	if len(rest) > 1 {
		when = rest[1]
	}

	switch cmd {
	case "":
		if once {
			fmt.Print(renderOnce(cfg, mustScan(cfg)))
			return
		}
		runTUI(cfg)
	case "daily", "day", "d":
		act := mustScan(cfg)
		deliver(cfg, RenderDaily(Build(act, cfg, Daily(parseDay(when))), md || slack), out, slack)
	case "weekly", "week", "w":
		act := mustScan(cfg)
		deliver(cfg, RenderWeekly(BuildWeekly(act, cfg, parseWeek(when)), md || slack), out, slack)
	case "tasks", "t":
		act := mustScan(cfg)
		deliver(cfg, RenderTasks(Build(act, cfg, Weekly(parseWeek(when))), md || slack), out, slack)
	case "scan":
		act := mustScan(cfg)
		total := 0
		for proj, set := range act.Minutes {
			fmt.Printf("%-30s %s\n", proj, hm(len(set)))
			total += len(set)
		}
		fmt.Printf("%-30s %s tracked all-time\n", "TOTAL", hm(total))
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func mustScan(cfg Config) *Activity {
	act, err := Scan(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	return act
}

// parseDay resolves the day selector; unknown input falls back to today.
func parseDay(s string) time.Time {
	now := time.Now()
	switch strings.ToLower(s) {
	case "", "today":
		return now
	case "yesterday", "y":
		return now.AddDate(0, 0, -1)
	}
	if n, ok := relative(s); ok {
		return now.AddDate(0, 0, n)
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t
	}
	return now
}

// parseWeek resolves the week selector; unknown input falls back to this week.
func parseWeek(s string) time.Time {
	now := time.Now()
	switch strings.ToLower(s) {
	case "", "this", "current":
		return now
	case "last", "prev", "previous":
		return now.AddDate(0, 0, -7)
	}
	if n, ok := relative(s); ok {
		return now.AddDate(0, 0, 7*n)
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t
	}
	return now
}

// relative parses "-2" / "+1" offsets shared by both selectors.
func relative(s string) (int, bool) {
	if len(s) < 2 || (s[0] != '-' && s[0] != '+') {
		return 0, false
	}
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func runTUI(cfg Config) {
	p := tea.NewProgram(newModel(cfg), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// deliver puts a rendered report where the flags asked for it. Stdout stays
// the default: a report is only published when the user types --slack.
func deliver(cfg Config, report, out string, slack bool) {
	printed := false
	if out != "" {
		path, err := writeOut(out, report)
		if err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Println("written to", path)
		printed = true
	}
	if slack {
		if err := postSlack(cfg.SlackWebhook, report); err != nil {
			fmt.Fprintln(os.Stderr, "slack:", err)
			os.Exit(1)
		}
		fmt.Println("posted to Slack")
		printed = true
	}
	if !printed {
		fmt.Print(report)
	}
}
