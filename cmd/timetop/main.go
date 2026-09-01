// timetop — autonomous time tracking. No timers to start: the work leaves a
// trail, this reads it. `timetop` opens the dashboard, `timetop weekly` and
// `timetop daily` print a report.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `timetop — autonomous time tracking from your work trail

  timetop                    dashboard (TUI)
  timetop daily [when]       when: today (default) | yesterday | YYYY-MM-DD
  timetop weekly [when]      when: this (default) | last | YYYY-MM-DD | -N weeks
  timetop tasks [when]       one line per branch: time, state, commits
  timetop watch              run the keyboard sensor in the foreground
  timetop watch --install    keep it running at login (launchd agent)
  timetop watch --stop       stop it and remove the agent
  timetop calendar [when]    the meetings a period contains (needs CALENDAR=1)
  timetop scan               refresh the cache and print what was found
  timetop prices             show the price table (from the claude CLI's own catalog)
  timetop prices --check     replay sessions and compare with Claude's own totals

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
	case "watch":
		switch when {
		case "--install", "install":
			if err := InstallDaemon(cfg); err != nil {
				fmt.Fprintln(os.Stderr, "install:", err)
				os.Exit(1)
			}
			fmt.Printf("sensor running and set to start at login\n"+
				"it records one line every %s: timestamp, seconds since your last input,\n"+
				"and the name of the app in front. Nothing else — no titles, no keystrokes.\n"+
				"log: %s\n", sampleInterval, cfg.SamplePath())
		case "--stop", "stop", "--uninstall":
			if err := RemoveDaemon(); err != nil {
				fmt.Fprintln(os.Stderr, "stop:", err)
				os.Exit(1)
			}
			fmt.Println("sensor stopped; the samples it already wrote are kept")
		case "--status", "status":
			if DaemonLoaded() {
				fmt.Println("sensor: running at login")
			} else {
				fmt.Println("sensor: not installed (timetop watch --install)")
			}
		default:
			fmt.Fprintf(os.Stderr, "sampling every %s — ctrl-c to stop\n", sampleInterval)
			if err := Watch(cfg); err != nil {
				fmt.Fprintln(os.Stderr, "watch:", err)
				os.Exit(1)
			}
		}
	case "calendar", "cal":
		if !cfg.Calendar {
			fmt.Printf("calendar reading is off — set CALENDAR=1 in %s\n"+
				"the first read asks macOS for calendar access; times and calendar names\n"+
				"are all that is stored, unless you also set CALENDAR_TITLES=1\n",
				filepath.Join(configDir(), "config.env"))
			return
		}
		p := Weekly(parseWeek(when))
		events, err := CalendarEvents(cfg, p.From, p.To)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		total := 0
		for _, e := range events {
			mark := " "
			if e.Meeting(cfg.MeetingMinutes) {
				mark = "•"
				total += int(e.End.Sub(e.Start).Minutes())
			}
			label := e.Calendar
			if e.Title != "" {
				label = e.Title
			}
			fmt.Printf("%s %s %s–%s %8s  %-28s %d people\n", mark, e.Start.Format("Mon 02 Jan"),
				e.Start.Format("15:04"), e.End.Format("15:04"),
				hm(int(e.End.Sub(e.Start).Minutes())), trunc(label, 28), e.People)
		}
		fmt.Printf("\n%d events, %s counted as worked time (• = counted)\n", len(events), hm(total))
	case "prices":
		pt := LoadPrices(cfg)
		if when == "--check" || when == "check" {
			if !pt.Known() {
				fmt.Fprintln(os.Stderr, "no price table configured — nothing to check")
				os.Exit(1)
			}
			fmt.Printf("checking %s against Claude Code's own cost records\n\n", pt.Source)
			fmt.Print(RenderCheck(CheckPrices(cfg, pt)))
			return
		}
		if !pt.Known() {
			fmt.Printf("no price table configured — reports show tokens, no dollars\n\n"+
				"point at one in %s:\n"+
				"  PRICES_FILE=~/model_prices.json   # LiteLLM-shaped, $ per token\n"+
				"  PRICES=claude-opus-5:15/75/1.5/18.75   # $ per Mtok: in/out/cache-read/cache-write\n",
				filepath.Join(configDir(), "config.env"))
			return
		}
		fmt.Printf("prices from %s\n\n", pt.Source)
		act := mustScan(cfg)
		seen := map[string]bool{}
		for _, byBucket := range act.Tokens {
			for bucket := range byBucket {
				seen[ModelOf(bucket)] = true
			}
		}
		for _, id := range pt.Models() {
			fmt.Printf("  %-28s %s\n", id, priceLine(pt, id))
		}
		var missing []string
		for model := range seen {
			if _, ok := pt.Cost(model, Tokens{}); !ok && model != "<synthetic>" {
				missing = append(missing, model)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			fmt.Printf("\nseen in your transcripts but unpriced: %s\n", strings.Join(missing, ", "))
		}
	case "scan":
		act := mustScan(cfg)
		type row struct {
			name string
			mins int
		}
		rows := make([]row, 0, len(act.Minutes))
		wall := map[minute]bool{}
		for proj, set := range act.Minutes {
			rows = append(rows, row{proj, len(set)})
			for m := range set {
				wall[m] = true
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].mins != rows[j].mins {
				return rows[i].mins > rows[j].mins
			}
			return rows[i].name < rows[j].name
		})
		for _, r := range rows {
			fmt.Printf("%-30s %s\n", r.name, hm(r.mins))
		}
		fmt.Printf("%-30s %s of wall clock, all time\n", "TOTAL", hm(len(wall)))
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

// priceLine renders one model's prices in the unit price lists use.
func priceLine(pt PriceTable, model string) string {
	mtok := Tokens{In: 1_000_000}
	in, _ := pt.Cost(model, mtok)
	out, _ := pt.Cost(model, Tokens{Out: 1_000_000})
	cr, _ := pt.Cost(model, Tokens{CacheR: 1_000_000})
	cw, _ := pt.Cost(model, Tokens{CacheW: 1_000_000})
	return fmt.Sprintf("in %s · out %s · cache read %s · cache write %s per Mtok",
		money(in), money(out), money(cr), money(cw))
}
