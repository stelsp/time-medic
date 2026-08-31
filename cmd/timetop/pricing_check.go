// A dollar figure nobody can audit is decoration. Claude Code writes its own
// running total into some transcripts; this replays those sessions through our
// price math and prints the difference, so the number can be trusted or
// caught being wrong.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type costCheck struct {
	Session string
	Ours    float64
	Theirs  float64
}

// CheckPrices replays every transcript that carries Claude Code's own cost
// record and prices it with the configured table.
func CheckPrices(cfg Config, pt PriceTable) []costCheck {
	var out []costCheck
	_ = filepath.WalkDir(cfg.TranscriptDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // unreadable corners are skipped
		}
		if c, ok := checkFile(path, pt); ok {
			out = append(out, c)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Theirs > out[j].Theirs })
	return out
}

func checkFile(path string, pt PriceTable) (costCheck, bool) {
	var ours, theirs float64
	found := false
	seen := map[string]bool{}

	// a session's own file plus every subagent transcript it spawned: the
	// parent carries the total, the children carry most of the tokens
	files := []string{path}
	sub := strings.TrimSuffix(path, ".jsonl")
	_ = filepath.WalkDir(sub, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})

	for _, fp := range files {
		f, err := os.Open(fp)
		if err != nil {
			continue
		}
		r := bufio.NewReaderSize(f, 1<<20)
		for {
			line, err := readLine(r)
			if len(line) > 0 {
				if fp == path && strings.Contains(string(line), `"type":"cost-state"`) {
					if v, ok := jsonFloat(line, "totalCostUSD"); ok {
						theirs = v // the last record in the file is the final total
						found = true
					}
				}
				if tk, bucket, id, ok := usageOf(line); ok && !seen[id] {
					if id != "" {
						seen[id] = true
					}
					if c, ok := pt.Cost(bucket, tk); ok {
						ours += c
					}
				}
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	if !found {
		return costCheck{}, false
	}
	return costCheck{Session: filepath.Base(filepath.Dir(path)) + "/" + strings.TrimSuffix(filepath.Base(path), ".jsonl"), Ours: ours, Theirs: theirs}, true
}

// RenderCheck prints the comparison and the overall drift.
func RenderCheck(checks []costCheck) string {
	if len(checks) == 0 {
		return "no session in your transcripts carries Claude Code's own cost record — nothing to check against\n"
	}
	var b strings.Builder
	var sumOurs, sumTheirs float64
	skipped := 0
	fmt.Fprintf(&b, "%-56s %10s %10s %8s\n", "SESSION", "OURS", "CLAUDE", "DIFF")
	for _, c := range checks {
		if c.Theirs == 0 {
			// a session Claude Code recorded no total for (plan-billed, or the
			// record was written before any call) proves nothing either way
			skipped++
			continue
		}
		sumOurs += c.Ours
		sumTheirs += c.Theirs
		fmt.Fprintf(&b, "%-56s %10s %10s %8s\n", trunc(c.Session, 56), money(c.Ours), money(c.Theirs), pct(c.Ours, c.Theirs))
	}
	if sumTheirs == 0 {
		return "every session with a cost record reports $0 — nothing to compare against\n"
	}
	fmt.Fprintf(&b, "\n%-56s %10s %10s %8s\n", "TOTAL", money(sumOurs), money(sumTheirs), pct(sumOurs, sumTheirs))
	if skipped > 0 {
		fmt.Fprintf(&b, "\n%d session(s) with a $0 record were left out of the comparison\n", skipped)
	}
	return b.String()
}

func pct(ours, theirs float64) string {
	if theirs == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.1f%%", 100*(ours-theirs)/theirs)
}

// jsonFloat is jsonNum for decimal values.
func jsonFloat(line []byte, key string) (float64, bool) {
	k := `"` + key + `":`
	i := strings.Index(string(line), k)
	if i < 0 {
		return 0, false
	}
	rest := string(line[i+len(k):])
	end := 0
	for end < len(rest) && (rest[end] == '.' || rest[end] == '-' || rest[end] == 'e' ||
		rest[end] == '+' || (rest[end] >= '0' && rest[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(rest[:end], "%g", &v); err != nil {
		return 0, false
	}
	return v, true
}
