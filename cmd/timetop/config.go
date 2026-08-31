// Config lives in one KEY=value file — same shape as merge-medic's config.env,
// readable by a shell script and by this binary without a parser library.
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	TranscriptDir string            // where Claude Code keeps session logs
	GapMinutes    int               // idle gap that ends a work session
	TargetHours   float64           // weekly target, drives the gauge
	Author        string            // git author filter (email or name)
	Aliases       map[string]string // raw project name -> reporting name
	StateDir      string
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
		TranscriptDir: filepath.Join(home, ".claude", "projects"),
		GapMinutes:    15,
		TargetHours:   40,
		Aliases:       map[string]string{},
		StateDir:      filepath.Join(configDir(), "state"),
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
		case "GIT_AUTHOR":
			cfg.Author = val
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

func (c Config) CachePath() string { return filepath.Join(c.StateDir, "cache.json") }

func expand(p, home string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
