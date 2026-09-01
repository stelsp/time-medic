// Meetings are work the keyboard cannot see: an hour on a call leaves no
// keystroke and no commit. The calendar already knows about it, so this reads
// it — through EventKit, the same door the Calendar app uses, and by default
// it takes only the shape of an event: when it started, when it ended, which
// calendar it came from. Not what it was called, not who was in it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Event is a calendar entry reduced to what a timesheet needs.
type Event struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Calendar string    `json:"calendar"` // the calendar's own name, often an address
	Source   string    `json:"source"`   // the account behind it: Google, iCloud, Exchange
	Busy     bool      `json:"busy"`
	AllDay   bool      `json:"allDay"`
	People   int       `json:"people"`
	Domains  []string  `json:"domains,omitempty"` // attendee domains only, never addresses
	Meet     string    `json:"meet,omitempty"`    // conferencing host, if the event has one
	Title    string    `json:"title,omitempty"`   // only with CALENDAR_TITLES=1
}

// Kind separates the work day from the rest of life. It reads the calendar it
// came from first, then the domains of the people in it — never the title, so
// the classification works with titles switched off.
func (e Event) Kind(cfg Config) string {
	for _, name := range cfg.PersonalCalendars {
		if strings.EqualFold(e.Calendar, name) {
			return "personal"
		}
	}
	for _, name := range cfg.WorkCalendars {
		if strings.EqualFold(e.Calendar, name) {
			return "work"
		}
	}
	for _, d := range e.Domains {
		for _, work := range cfg.WorkDomains {
			if strings.EqualFold(d, work) {
				return "work"
			}
		}
	}
	// a solo entry in an unnamed calendar is a block in a diary, not a meeting
	if len(cfg.WorkCalendars) > 0 || len(cfg.WorkDomains) > 0 {
		return "personal"
	}
	return "unknown"
}

// Counts reports whether this event should be billed as worked time.
func (e Event) Counts(cfg Config) bool {
	if !e.Meeting(cfg.MeetingMinutes) {
		return false
	}
	switch e.Kind(cfg) {
	case "personal":
		return cfg.CountPersonal
	default:
		return true
	}
}

// Meeting reports whether an event is time actually spent, as opposed to a
// day marker, a reminder, or something the user declined to attend.
func (e Event) Meeting(minMinutes int) bool {
	if e.AllDay || !e.Busy {
		return false
	}
	return int(e.End.Sub(e.Start).Minutes()) >= minMinutes
}

// Minutes are the worked minutes an event covers.
func (e Event) Minutes() []minute {
	var out []minute
	for t := e.Start; t.Before(e.End); t = t.Add(time.Minute) {
		out = append(out, minute(t.Unix()/60))
	}
	return out
}

// CalendarEvents returns the events in a window, from cache when the window
// was already fetched, so the permission dialog and EventKit are touched once.
func CalendarEvents(cfg Config, from, to time.Time) ([]Event, error) {
	if !cfg.Calendar {
		return nil, nil
	}
	cached, ok := readEventCache(cfg, from, to)
	if ok {
		return cached, nil
	}
	events, err := fetchEvents(cfg, from, to)
	if err != nil {
		return nil, err
	}
	writeEventCache(cfg, from, to, events)
	return events, nil
}

func eventCachePath(cfg Config) string { return filepath.Join(cfg.StateDir, "calendar.json") }

type eventCache struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Fetched time.Time `json:"fetched"`
	Titles  bool      `json:"titles"`
	Events  []Event   `json:"events"`
}

// readEventCache serves a window already covered by a recent fetch. Past days
// never change; only the window's tail is worth re-reading, so a cache older
// than an hour is refused.
func readEventCache(cfg Config, from, to time.Time) ([]Event, bool) {
	data, err := os.ReadFile(eventCachePath(cfg))
	if err != nil {
		return nil, false
	}
	var c eventCache
	if json.Unmarshal(data, &c) != nil {
		return nil, false
	}
	if c.Titles != cfg.CalendarTitles || c.From.After(from) || c.To.Before(to) ||
		time.Since(c.Fetched) > time.Hour {
		return nil, false
	}
	out := make([]Event, 0, len(c.Events))
	for _, e := range c.Events {
		if e.End.After(from) && e.Start.Before(to) {
			out = append(out, e)
		}
	}
	return out, true
}

func writeEventCache(cfg Config, from, to time.Time, events []Event) {
	data, err := json.Marshal(eventCache{From: from, To: to, Fetched: time.Now(),
		Titles: cfg.CalendarTitles, Events: events})
	if err != nil || os.MkdirAll(cfg.StateDir, 0o755) != nil {
		return
	}
	tmp := eventCachePath(cfg) + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, eventCachePath(cfg))
	}
}

// fetchEvents runs the EventKit helper, building it the first time. macOS asks
// for calendar access on that first run; it never asks again.
func fetchEvents(cfg Config, from, to time.Time) ([]Event, error) {
	helper, err := ensureHelper(cfg)
	if err != nil {
		return nil, err
	}
	titles := "0"
	if cfg.CalendarTitles {
		titles = "1"
	}
	cmd := exec.Command(helper, from.Format(time.RFC3339), to.Format(time.RFC3339), titles)
	cmd.Stdin = os.Stdin // so the permission dialog attaches to your session
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "refused") {
			return nil, fmt.Errorf("calendar: %s\n"+
				"  macOS asks the app that started timetop, not timetop itself.\n"+
				"  Run `timetop calendar` once in your own terminal and click Allow;\n"+
				"  if no dialog appears, tick your terminal under\n"+
				"  System Settings → Privacy & Security → Calendars.", msg)
		}
		return nil, fmt.Errorf("calendar: %s", msg)
	}
	var events []Event
	if err := json.Unmarshal(out, &events); err != nil {
		return nil, fmt.Errorf("calendar: unreadable answer: %w", err)
	}
	if len(cfg.Calendars) > 0 {
		kept := events[:0]
		for _, e := range events {
			for _, name := range cfg.Calendars {
				if strings.EqualFold(e.Calendar, name) {
					kept = append(kept, e)
					break
				}
			}
		}
		events = kept
	}
	return events, nil
}

// ensureHelper compiles the EventKit reader once and keeps it next to the
// state, rebuilding it whenever this source changes.
func ensureHelper(cfg Config) (string, error) {
	dir := filepath.Join(cfg.StateDir, "helper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src := filepath.Join(dir, "calendar.swift")
	bin := filepath.Join(dir, "calendar")
	current, _ := os.ReadFile(src)
	if string(current) != calendarSwift {
		if err := os.WriteFile(src, []byte(calendarSwift), 0o644); err != nil {
			return "", err
		}
		_ = os.Remove(bin)
	}
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if _, err := exec.LookPath("swiftc"); err != nil {
		return "", fmt.Errorf("calendar needs swiftc (install Xcode command line tools) — or turn CALENDAR off")
	}
	build := exec.Command("swiftc", "-O", "-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the calendar reader: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return bin, nil
}

// calendarSwift is the whole helper: ask for access, list events in a window,
// print the shape of each one. Titles only when explicitly asked for.
const calendarSwift = `import EventKit
import Foundation

let args = CommandLine.arguments
guard args.count >= 3 else {
    FileHandle.standardError.write("usage: calendar <fromISO> <toISO> [withTitles]\n".data(using: .utf8)!)
    exit(64)
}
let iso = ISO8601DateFormatter()
guard let from = iso.date(from: args[1]), let to = iso.date(from: args[2]) else {
    FileHandle.standardError.write("bad dates\n".data(using: .utf8)!)
    exit(64)
}
let withTitles = args.count > 3 && args[3] == "1"

let store = EKEventStore()
var granted = false
let sem = DispatchSemaphore(value: 0)
if #available(macOS 14.0, *) {
    store.requestFullAccessToEvents { ok, _ in granted = ok; sem.signal() }
} else {
    store.requestAccess(to: .event) { ok, _ in granted = ok; sem.signal() }
}
_ = sem.wait(timeout: .now() + 60)
guard granted else {
    FileHandle.standardError.write("access to Calendar was refused — grant it in System Settings > Privacy & Security > Calendars\n".data(using: .utf8)!)
    exit(2)
}

let predicate = store.predicateForEvents(withStart: from, end: to, calendars: nil)
var rows: [[String: Any]] = []
for event in store.events(matching: predicate) {
    guard let start = event.startDate, let end = event.endDate else { continue }
    // an event the user declined is somebody else's meeting, not their time
    var declined = false
    for attendee in event.attendees ?? [] where attendee.isCurrentUser {
        declined = attendee.participantStatus == .declined
    }
    if declined { continue }
    // attendee domains, never addresses: enough to tell a work meeting from a
    // dentist appointment, useless for anything else
    var domains: Set<String> = []
    for attendee in event.attendees ?? [] {
        guard let url = attendee.url as URL?, url.scheme == "mailto" else { continue }
        let address = url.absoluteString.replacingOccurrences(of: "mailto:", with: "")
        if let at = address.firstIndex(of: "@") {
            domains.insert(String(address[address.index(after: at)...]).lowercased())
        }
    }
    var meet = ""
    if let host = event.url?.host { meet = host }
    var row: [String: Any] = [
        "start": iso.string(from: start),
        "end": iso.string(from: end),
        "calendar": event.calendar?.title ?? "",
        "source": event.calendar?.source?.title ?? "",
        "busy": event.availability != .free,
        "allDay": event.isAllDay,
        "people": event.attendees?.count ?? 0,
        "domains": Array(domains),
        "meet": meet,
    ]
    if withTitles { row["title"] = event.title ?? "" }
    rows.append(row)
}
let data = try JSONSerialization.data(withJSONObject: rows, options: [])
FileHandle.standardOutput.write(data)
`

// RenderCalendars shows what the account actually holds, so the work ones can
// be named in the config instead of guessed at by a heuristic.
func RenderCalendars(cfg Config, events []Event) string {
	type stat struct {
		name, source string
		events, mins int
		domains      map[string]int
	}
	byCal := map[string]*stat{}
	for _, e := range events {
		key := e.Source + "\x1f" + e.Calendar
		s := byCal[key]
		if s == nil {
			s = &stat{name: e.Calendar, source: e.Source, domains: map[string]int{}}
			byCal[key] = s
		}
		s.events++
		s.mins += int(e.End.Sub(e.Start).Minutes())
		for _, d := range e.Domains {
			s.domains[d]++
		}
	}
	stats := make([]*stat, 0, len(byCal))
	for _, s := range byCal {
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].mins != stats[j].mins {
			return stats[i].mins > stats[j].mins
		}
		return stats[i].name < stats[j].name
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%-34s %-12s %7s %9s  %s\n", "CALENDAR", "ACCOUNT", "EVENTS", "TIME", "WHO YOU MEET")
	for _, s := range stats {
		doms := make([]string, 0, len(s.domains))
		for d := range s.domains {
			doms = append(doms, d)
		}
		sort.Slice(doms, func(i, j int) bool { return s.domains[doms[i]] > s.domains[doms[j]] })
		if len(doms) > 3 {
			doms = doms[:3]
		}
		fmt.Fprintf(&b, "%-34s %-12s %7d %9s  %s\n", trunc(s.name, 34), trunc(s.source, 12),
			s.events, hm(s.mins), strings.Join(doms, ", "))
	}
	fmt.Fprint(&b, "\nname the work ones in "+filepath.Join(configDir(), "config.env")+":\n"+
		"  WORK_CALENDARS=<calendar>,<calendar>\n"+
		"  WORK_DOMAINS=<the domain your colleagues use>\n"+
		"anything else is treated as personal and left out of the totals\n"+
		"(COUNT_PERSONAL=1 counts it too).\n")
	return b.String()
}
