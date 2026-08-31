// Autonomous activity capture: Claude Code session transcripts are the clock.
// Every timestamped entry in ~/.claude/projects/**/*.jsonl is a proof of work
// at a known cwd and branch; minutes between two nearby entries count as
// worked minutes. Token counts ride along from the same lines.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cacheVersion invalidates every cached transcript when the derived shape
// changes — cheaper than migrating, and a full rescan is under a second.
const cacheVersion = 4

// minute index = unix seconds / 60; rendering converts to local time.
type minute int64

// taskKey pairs a project with the branch it was worked on. The separator is
// a unit separator so it can never collide with a branch name.
const taskSep = "\x1f"

func taskKey(proj, branch string) string { return proj + taskSep + branch }

func splitTask(key string) (proj, branch string) {
	proj, branch, _ = strings.Cut(key, taskSep)
	return proj, branch
}

// Tokens is what an AI call cost in units nobody has to price: raw counts.
// Cache writes are split by TTL because they are billed at different rates.
type Tokens struct {
	In        int64 `json:"i"`
	Out       int64 `json:"o"`
	CacheR    int64 `json:"r"`
	CacheW    int64 `json:"w"`
	CacheW1h  int64 `json:"w1h"`
	WebSearch int64 `json:"ws"`
	Calls     int64 `json:"n"`
}

func (t *Tokens) add(o Tokens) {
	t.In += o.In
	t.Out += o.Out
	t.CacheR += o.CacheR
	t.CacheW += o.CacheW
	t.CacheW1h += o.CacheW1h
	t.WebSearch += o.WebSearch
	t.Calls += o.Calls
}

// Total is every token the model actually read or wrote.
func (t Tokens) Total() int64 { return t.In + t.Out + t.CacheR + t.CacheW }

// bucketKey groups tokens by everything that changes their price: the model,
// the speed tier and the inference region.
func bucketKey(model, speed, geo string) string {
	return model + taskSep + speed + taskSep + geo
}

func splitBucket(key string) (model, speed, geo string) {
	parts := strings.SplitN(key, taskSep, 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

// ModelOf is the display name of a token bucket.
func ModelOf(bucket string) string {
	m, _, _ := splitBucket(bucket)
	return m
}

// Activity is the union of every source: worked minutes per project and per
// task, the tokens burned, and the checkouts to ask about commits.
type Activity struct {
	Minutes map[string]map[minute]bool // project -> active minutes
	Tasks   map[string]map[minute]bool // project\x1fbranch -> active minutes
	// Agent holds the minutes that came from unattended runs (a bot calling
	// claude -p), so a report can separate your hours from your robots'.
	Agent  map[minute]bool
	Human  map[minute]bool
	Roots  map[string]string // project -> a real checkout path
	Tokens map[string]map[string]*Tokens
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
	V       int                          `json:"v"`
	Size    int64                        `json:"size"`
	ModTime int64                        `json:"mtime"`
	Gap     int                          `json:"gap"`
	Minutes map[string][]int64           `json:"minutes"` // task key -> minutes
	Agent   []int64                      `json:"agent"`   // unattended minutes
	Human   []int64                      `json:"human"`
	Roots   map[string]string            `json:"roots"`
	Tokens  map[string]map[string]Tokens `json:"tokens"` // day -> model -> counts
}

type scanCache struct {
	Files map[string]*fileCache `json:"files"`
}

// Scan walks the transcript tree and returns every worked minute it can prove.
func Scan(cfg Config) (*Activity, error) {
	cache := loadCache(cfg.CachePath())
	act := &Activity{
		Minutes: map[string]map[minute]bool{},
		Tasks:   map[string]map[minute]bool{},
		Agent:   map[minute]bool{},
		Human:   map[minute]bool{},
		Roots:   map[string]string{},
		Tokens:  map[string]map[string]*Tokens{},
	}
	dirty := false
	// one project can be seen through several checkouts (a duty clone, a dead
	// worktree, the dev tree): the one most sessions ran in is the one to ask
	// about commits
	rootVotes := map[string]map[string]int{}
	err := filepath.WalkDir(cfg.TranscriptDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // unreadable corners are skipped, not fatal
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil
		}
		fc := cache.Files[path]
		if fc == nil || fc.V != cacheVersion || fc.Size != st.Size() ||
			fc.ModTime != st.ModTime().Unix() || fc.Gap != cfg.GapMinutes {
			fc = scanFile(path, cfg)
			cache.Files[path] = fc
			dirty = true
		}
		for key, mins := range fc.Minutes {
			proj, _ := splitTask(key)
			addMinutes(act.Tasks, key, mins)
			addMinutes(act.Minutes, proj, mins)
		}
		for _, m := range fc.Agent {
			act.Agent[minute(m)] = true
		}
		for _, m := range fc.Human {
			act.Human[minute(m)] = true
		}
		for proj, root := range fc.Roots {
			if rootVotes[proj] == nil {
				rootVotes[proj] = map[string]int{}
			}
			rootVotes[proj][root]++
		}
		for day, byModel := range fc.Tokens {
			if act.Tokens[day] == nil {
				act.Tokens[day] = map[string]*Tokens{}
			}
			for model, tk := range byModel {
				if act.Tokens[day][model] == nil {
					act.Tokens[day][model] = &Tokens{}
				}
				act.Tokens[day][model].add(tk)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for proj, votes := range rootVotes {
		act.Roots[proj] = bestRoot(votes)
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

func addMinutes(dst map[string]map[minute]bool, key string, mins []int64) {
	set := dst[key]
	if set == nil {
		set = map[minute]bool{}
		dst[key] = set
	}
	for _, m := range mins {
		set[minute(m)] = true
	}
}

// scanFile turns one transcript into gap-filled minutes per task. Lines are
// read raw and only the fields we need are cut out by hand: a transcript line
// can be megabytes of tool output, and JSON-decoding all of it is 20x slower.
func scanFile(path string, cfg Config) *fileCache {
	fc := &fileCache{
		V: cacheVersion, Gap: cfg.GapMinutes,
		Minutes: map[string][]int64{},
		Roots:   map[string]string{},
		Tokens:  map[string]map[string]Tokens{},
	}
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

	byTask := map[string][]int64{}
	var agent, human []int64
	// one API call writes several assistant rows; requestId is what makes them
	// one call again, and counting rows would inflate every token number
	seenCall := map[string]bool{}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			ts := jsonField(line, "timestamp")
			cwd := jsonField(line, "cwd")
			if ts != "" && cwd != "" {
				if t, e := time.Parse(time.RFC3339, ts); e == nil {
					proj, root := projectOf(cwd, cfg)
					key := taskKey(proj, jsonField(line, "gitBranch"))
					byTask[key] = append(byTask[key], t.Unix()/60)
					if unattended(jsonField(line, "entrypoint")) {
						agent = append(agent, t.Unix()/60)
					} else {
						human = append(human, t.Unix()/60)
					}
					if _, ok := fc.Roots[proj]; !ok {
						fc.Roots[proj] = root
					}
					if tk, bucket, id, ok := usageOf(line); ok && !seenCall[id] {
						if id != "" {
							seenCall[id] = true
						}
						day := t.Local().Format("2006-01-02")
						if fc.Tokens[day] == nil {
							fc.Tokens[day] = map[string]Tokens{}
						}
						cur := fc.Tokens[day][bucket]
						cur.add(tk)
						fc.Tokens[day][bucket] = cur
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	for key, ts := range byTask {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		fc.Minutes[key] = fillGaps(ts, int64(cfg.GapMinutes))
	}
	sort.Slice(agent, func(i, j int) bool { return agent[i] < agent[j] })
	sort.Slice(human, func(i, j int) bool { return human[i] < human[j] })
	fc.Agent = fillGaps(agent, int64(cfg.GapMinutes))
	fc.Human = fillGaps(human, int64(cfg.GapMinutes))
	return fc
}

// unattended tells a robot's session from yours: the SDK entrypoints are what
// a script gets when it shells out to claude -p.
func unattended(entrypoint string) bool {
	return strings.HasPrefix(entrypoint, "sdk-")
}

// usageOf reads the token counts off an assistant line. The first occurrence
// of each key inside the usage object is the message-level count; the
// per-iteration copies that follow are the same tokens counted again. The
// returned id is the API call this row belongs to, for de-duplication.
func usageOf(line []byte) (tk Tokens, bucket, id string, ok bool) {
	i := strings.Index(string(line), `"usage":{`)
	if i < 0 {
		return Tokens{}, "", "", false
	}
	rest := line[i:]
	tk = Tokens{
		In:        jsonNum(rest, "input_tokens"),
		Out:       jsonNum(rest, "output_tokens"),
		CacheR:    jsonNum(rest, "cache_read_input_tokens"),
		CacheW:    jsonNum(rest, "cache_creation_input_tokens"),
		CacheW1h:  jsonNum(rest, "ephemeral_1h_input_tokens"),
		WebSearch: jsonNum(rest, "web_search_requests"),
		Calls:     1,
	}
	model := jsonField(line, "model")
	if model == "" {
		model = "unknown"
	}
	id = jsonField(line, "requestId")
	if id == "" {
		id = jsonField(line, "id") // message id, when the request id is absent
	}
	return tk, bucketKey(model, jsonField(rest, "speed"), jsonField(rest, "inference_geo")), id, true
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

// jsonNum is jsonField for unquoted numbers.
func jsonNum(line []byte, key string) int64 {
	k := `"` + key + `":`
	i := strings.Index(string(line), k)
	if i < 0 {
		return 0
	}
	rest := string(line[i+len(k):])
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, _ := strconv.ParseInt(rest[:end], 10, 64)
	return n
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
	} else if home, err := os.UserHomeDir(); err == nil && filepath.Clean(cwd) == filepath.Clean(home) {
		name = "misc" // a session started from $HOME belongs to no project
	}
	// a deleted worktree leaves no repo to ask, so the directory name stands
	name = strings.TrimPrefix(name, ".")
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
		url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
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

// bestRoot picks the checkout to ask about a project's commits: the one most
// sessions ran in, and among equals one that still exists on disk.
func bestRoot(votes map[string]int) string {
	best, bestScore := "", -1
	for root, n := range votes {
		score := n * 2
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			score++
		} else {
			score -= 1000 // a path that is gone cannot answer git questions
		}
		if score > bestScore || (score == bestScore && root < best) {
			best, bestScore = root, score
		}
	}
	return best
}
