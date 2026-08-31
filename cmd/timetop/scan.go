// Autonomous activity capture: Claude Code session transcripts are the clock.
// Every timestamped entry in ~/.claude/projects/**/*.jsonl is a proof of work
// at a known cwd; minutes between two nearby entries count as worked minutes.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// minute index = unix seconds / 60, in UTC; rendering converts to local time.
type minute int64

// Activity is the union of every source: worked minutes per project plus the
// artifacts (commits) that landed inside them.
type Activity struct {
	Minutes map[string]map[minute]bool // project -> active minutes
	Roots   map[string]string          // project -> a real checkout path
	Commits map[string][]Commit        // project -> commits (filled on demand)
	// First is the oldest minute on record: nothing before it can be reported.
	First time.Time
}

type Commit struct {
	SHA     string
	When    time.Time
	Subject string
	Branch  string
}

// fileCache is what we remember about one transcript so a rescan of an
// unchanged file costs nothing.
type fileCache struct {
	Size    int64              `json:"size"`
	ModTime int64              `json:"mtime"`
	Gap     int                `json:"gap"`
	Minutes map[string][]int64 `json:"minutes"` // project -> minute indices
	Roots   map[string]string  `json:"roots"`
}

type scanCache struct {
	Files map[string]*fileCache `json:"files"`
}

// Scan walks the transcript tree and returns every worked minute it can prove.
func Scan(cfg Config) (*Activity, error) {
	cache := loadCache(cfg.CachePath())
	act := &Activity{
		Minutes: map[string]map[minute]bool{},
		Roots:   map[string]string{},
		Commits: map[string][]Commit{},
	}
	dirty := false
	err := filepath.WalkDir(cfg.TranscriptDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // unreadable corners are skipped, not fatal
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil
		}
		fc := cache.Files[path]
		if fc == nil || fc.Size != st.Size() || fc.ModTime != st.ModTime().Unix() || fc.Gap != cfg.GapMinutes {
			fc = scanFile(path, cfg)
			cache.Files[path] = fc
			dirty = true
		}
		for proj, mins := range fc.Minutes {
			set := act.Minutes[proj]
			if set == nil {
				set = map[minute]bool{}
				act.Minutes[proj] = set
			}
			for _, m := range mins {
				set[minute(m)] = true
			}
		}
		for proj, root := range fc.Roots {
			if _, ok := act.Roots[proj]; !ok {
				act.Roots[proj] = root
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, set := range act.Minutes {
		for m := range set {
			t := time.Unix(int64(m)*60, 0)
			if act.First.IsZero() || t.Before(act.First) {
				act.First = t
			}
		}
	}
	if dirty {
		saveCache(cfg.CachePath(), cache)
	}
	return act, nil
}

// scanFile turns one transcript into gap-filled minutes per project. Lines are
// read raw and the two fields we need are cut out by hand: a transcript line
// can be megabytes of tool output, and JSON-decoding all of it is 20x slower.
func scanFile(path string, cfg Config) *fileCache {
	fc := &fileCache{Gap: cfg.GapMinutes, Minutes: map[string][]int64{}, Roots: map[string]string{}}
	st, err := os.Stat(path)
	if err != nil {
		return fc
	}
	fc.Size, fc.ModTime = st.Size(), st.ModTime().Unix()

	f, err := os.Open(path)
	if err != nil {
		return fc
	}
	defer f.Close()

	type ev struct {
		t    int64
		proj string
	}
	var evs []ev
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			ts := jsonField(line, "timestamp")
			cwd := jsonField(line, "cwd")
			if ts != "" && cwd != "" {
				if t, e := time.Parse(time.RFC3339, ts); e == nil {
					proj, root := projectOf(cwd, cfg)
					evs = append(evs, ev{t.Unix() / 60, proj})
					if _, ok := fc.Roots[proj]; !ok {
						fc.Roots[proj] = root
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	byProj := map[string][]int64{}
	for _, e := range evs {
		byProj[e.proj] = append(byProj[e.proj], e.t)
	}
	for proj, ts := range byProj {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		fc.Minutes[proj] = fillGaps(ts, int64(cfg.GapMinutes))
	}
	return fc
}

// fillGaps marks every minute between two events that sit within the idle gap,
// so a session reads as continuous work instead of a dotted line of pings.
// A lone event still counts as one worked minute.
func fillGaps(ts []int64, gap int64) []int64 {
	out := make([]int64, 0, len(ts))
	var last int64 = -1
	for _, t := range ts {
		if last >= 0 && t-last <= gap && t-last > 1 {
			for m := last + 1; m < t; m++ {
				out = append(out, m)
			}
		}
		if t != last {
			out = append(out, t)
		}
		last = t
	}
	return out
}

// readLine returns one whole line however long it is (bufio.Scanner caps out).
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, err
	}
}

// jsonField cuts "key":"value" out of a raw line without parsing the object.
func jsonField(line []byte, key string) string {
	k := `"` + key + `":"`
	i := strings.Index(string(line), k)
	if i < 0 {
		return ""
	}
	rest := string(line[i+len(k):])
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// projectOf names the unit of work behind a cwd: worktrees, duty clones and
// subdirectories of one repo all report as the same project, so a week's time
// lands on "ai-viewer-proto", not on twelve feature branches.
func projectOf(cwd string, cfg Config) (name, root string) {
	root, name = cwd, filepath.Base(cwd)
	parts := strings.Split(cwd, string(os.PathSeparator))
	for i := len(parts) - 1; i > 0; i-- {
		p := parts[i]
		if p == "worktrees" || p == ".worktrees" || strings.HasSuffix(p, "-worktrees") {
			root = strings.Join(parts[:i], string(os.PathSeparator))
			name = filepath.Base(root)
			break
		}
	}
	if top := gitTopLevel(root); top != "" {
		root = top
		name = filepath.Base(top)
		// the remote is the identity: a duty clone, a fleet instance and the
		// dev checkout of one repo are one project, whatever they are called
		// on disk
		if rn := gitRemoteName(top); rn != "" {
			name = rn
		}
	} else {
		name = "misc"
	}
	name = strings.TrimPrefix(name, ".")
	for _, suf := range []string{"-repo", "-gh"} {
		if trimmed := strings.TrimSuffix(name, suf); trimmed != "" && trimmed != name {
			name = trimmed
		}
	}
	if alias, ok := cfg.Aliases[name]; ok {
		name = alias
	}
	return name, root
}

var topLevels = map[string]string{}
var remoteNames = map[string]string{}

// gitRemoteName is the repo name as the forge knows it, memoized per root.
func gitRemoteName(root string) string {
	if v, ok := remoteNames[root]; ok {
		return v
	}
	name := ""
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		url := strings.TrimSpace(string(out))
		url = strings.TrimSuffix(url, ".git")
		if i := strings.LastIndexAny(url, "/:"); i >= 0 {
			name = url[i+1:]
		}
	}
	remoteNames[root] = name
	return name
}

// gitTopLevel resolves a path to its repository root (empty if the path is
// gone or untracked). Results are memoized: one exec per distinct cwd.
func gitTopLevel(path string) string {
	if v, ok := topLevels[path]; ok {
		return v
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	top := ""
	if err == nil {
		top = strings.TrimSpace(string(out))
	}
	topLevels[path] = top
	return top
}

func loadCache(path string) *scanCache {
	c := &scanCache{Files: map[string]*fileCache{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	if json.Unmarshal(data, c) != nil || c.Files == nil {
		return &scanCache{Files: map[string]*fileCache{}}
	}
	return c
}

func saveCache(path string, c *scanCache) {
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}
