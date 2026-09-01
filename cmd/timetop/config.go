// Config lives in one KEY=value file — same shape as merge-medic's config.env,
// readable by a shell script and by this binary without a parser library.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	TranscriptDir     string            // where Claude Code keeps session logs
	GapMinutes        int               // idle gap that ends a work session
	TargetHours       float64           // weekly target, drives the gauge
	Author            string            // git author filter (email or name)
	Aliases           map[string]string // raw project name -> reporting name
	SlackWebhook      string            // incoming webhook, used only with --slack
	IdleSeconds       int               // input older than this means nobody was there
	Calendar          bool              // read meetings from the calendar at all
	CalendarTitles    bool              // include event titles (off: only times and shape)
	Calendars         []string          // only these calendars, empty means all
	WorkCalendars     []string          // calendars whose events are work
	PersonalCalendars []string          // calendars whose events are not
	WorkDomains       []string          // attendee domains that make a meeting work
	CountPersonal     bool              // count personal events as worked time too
	MeetingMinutes    int               // shorter events are not counted as worked time
	AppCategories     map[string]string // app name -> what that time is
	Prices            string            // inline price table, dollars per Mtok
	PricesFile        string            // path to a JSON price table (LiteLLM shape works)
	StateDir          string
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "timetop")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "timetop")
}

func LoadConfig() Config {
	home, _ := os.UserHomeDir()
	cfg := Config{
		TranscriptDir:  filepath.Join(home, ".claude", "projects"),
		GapMinutes:     15,
		TargetHours:    40,
		IdleSeconds:    180,
		MeetingMinutes: 10,
		AppCategories:  map[string]string{},
		Aliases:        map[string]string{},
		StateDir:       filepath.Join(configDir(), "state"),
	}
	data, err := os.ReadFile(filepath.Join(configDir(), "config.env"))
	if err != nil {
		return cfg
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		key, val, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch strings.TrimSpace(key) {
		case "TRANSCRIPT_DIR":
			cfg.TranscriptDir = expand(val, home)
		case "GAP_MINUTES":
			if n, e := strconv.Atoi(val); e == nil && n > 0 {
				cfg.GapMinutes = n
			}
		case "TARGET_HOURS":
			if f, e := strconv.ParseFloat(val, 64); e == nil && f > 0 {
				cfg.TargetHours = f
			}
		case "CALENDAR":
			cfg.Calendar = val == "1" || strings.EqualFold(val, "true")
		case "CALENDAR_TITLES":
			cfg.CalendarTitles = val == "1" || strings.EqualFold(val, "true")
		case "CALENDARS":
			cfg.Calendars = splitList(val)
		case "WORK_CALENDARS":
			cfg.WorkCalendars = splitList(val)
		case "PERSONAL_CALENDARS":
			cfg.PersonalCalendars = splitList(val)
		case "WORK_DOMAINS":
			cfg.WorkDomains = splitList(val)
		case "COUNT_PERSONAL":
			cfg.CountPersonal = val == "1" || strings.EqualFold(val, "true")
		case "MEETING_MINUTES":
			if n, e := strconv.Atoi(val); e == nil && n >= 0 {
				cfg.MeetingMinutes = n
			}
		case "IDLE_SECONDS":
			if n, e := strconv.Atoi(val); e == nil && n > 0 {
				cfg.IdleSeconds = n
			}
		case "APP_CATEGORIES": // "zoom.us:meeting,Slack:comms"
			for _, pair := range strings.Split(val, ",") {
				if app, cat, ok := strings.Cut(strings.TrimSpace(pair), ":"); ok {
					cfg.AppCategories[app] = cat
				}
			}
		case "GIT_AUTHOR":
			cfg.Author = val
		case "SLACK_WEBHOOK":
			cfg.SlackWebhook = val
		case "PRICES":
			cfg.Prices = val
		case "PRICES_FILE":
			cfg.PricesFile = val
		case "ALIASES": // "raw:name,raw2:name2"
			for _, pair := range strings.Split(val, ",") {
				if from, to, ok := strings.Cut(strings.TrimSpace(pair), ":"); ok {
					cfg.Aliases[from] = to
				}
			}
		}
	}
	return cfg
}

// CachePath is version-stamped: two binaries with different derived shapes
// never read each other's cache, they just rescan.
func (c Config) CachePath() string {
	return filepath.Join(c.StateDir, fmt.Sprintf("cache-v%d.json", cacheVersion))
}

func expand(p, home string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// splitList reads a comma-separated config value, dropping empty entries.
func splitList(val string) []string {
	var out []string
	for _, item := range strings.Split(val, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
