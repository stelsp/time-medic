// Autonomous activity capture: Claude Code session transcripts are the clock.
// Every timestamped entry in ~/.claude/projects/**/*.jsonl is a proof of work
// at a known cwd and branch; minutes between two nearby entries count as
// worked minutes. Token counts ride along from the same lines.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cacheVersion invalidates every cached transcript when the derived shape
// changes — cheaper than migrating, and a full rescan is under a second.
const cacheVersion = 5

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
	Agent map[minute]bool
	Human map[minute]bool
	// Apps holds the minutes the keyboard sensor saw, by application. They
	// prove presence, not project: the transcripts say what was worked on.
	Apps map[string]map[minute]bool
	// Events are calendar entries — the work that leaves no keystroke.
	Events      []Event
	CalendarErr error             // why there are none, when the calendar was asked for
	Roots       map[string]string // project -> a real checkout path
	Tokens      map[string]map[string]*Tokens
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
	V       int                `json:"v"`
	Env     string             `json:"env"` // timezone and aliases are baked into the values below
	Size    int64              `json:"size"`
	ModTime int64              `json:"mtime"`
	Gap     int                `json:"gap"`
	Minutes map[string][]int64 `json:"minutes"` // task key -> minutes
	Agent   []int64            `json:"agent"`   // unattended minutes
	Human   []int64            `json:"human"`
	Roots   map[string]string  `json:"roots"`
	Calls   []Call             `json:"calls"` // one record per API call, for cross-file de-duplication
}

// Call is one API call as the transcripts record it. Sessions get forked and
// resumed, which copies rows into a new file; the id is what keeps a call from
// being counted twice.
type Call struct {
	ID     string `json:"id"`
	Day    string `json:"d"`
	Bucket string `json:"b"`
	T      Tokens `json:"t"`
}

type scanCache struct {
	Files map[string]*fileCache `json:"files"`
}

// Scan walks the transcript tree and returns every worked minute it can prove.
func Scan(cfg Config) (*Activity, error) {
	if st, err := os.Stat(cfg.TranscriptDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("no transcripts at %s — set TRANSCRIPT_DIR in %s",
			cfg.TranscriptDir, filepath.Join(configDir(), "config.env"))
	}
	cache := loadCache(cfg.CachePath())
	act := &Activity{
		Minutes: map[string]map[minute]bool{},
		Tasks:   map[string]map[minute]bool{},
		Agent:   map[minute]bool{},
		Human:   map[minute]bool{},
		Apps:    map[string]map[minute]bool{},
		Roots:   map[string]string{},
		Tokens:  map[string]map[string]*Tokens{},
	}
	dirty := false
	// one project can be seen through several checkouts (a duty clone, a dead
	// worktree, the dev tree): the one most sessions ran in is the one to ask
	// about commits
	rootVotes := map[string]map[string]int{}
	seenCalls := map[string]bool{}
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
			fc.ModTime != st.ModTime().Unix() || fc.Gap != cfg.GapMinutes ||
			fc.Env != cacheEnv(cfg) {
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
		for _, c := range fc.Calls {
			if c.ID != "" {
				if seenCalls[c.ID] {
					continue
				}
				seenCalls[c.ID] = true
			}
			if act.Tokens[c.Day] == nil {
				act.Tokens[c.Day] = map[string]*Tokens{}
			}
			if act.Tokens[c.Day][c.Bucket] == nil {
				act.Tokens[c.Day][c.Bucket] = &Tokens{}
			}
			act.Tokens[c.Day][c.Bucket].add(c.T)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for proj, votes := range rootVotes {
		act.Roots[proj] = bestRoot(votes)
	}
	if apps, err := ReadSamples(cfg); err == nil {
		act.Apps = apps
	}
	if cfg.Calendar {
		// one window, cached: reports slice their own period out of it
		now := time.Now()
		// a refused calendar is not a scan failure: reports carry on without
		// meetings, and `timetop calendar` is where the reason is shown
		if events, err := CalendarEvents(cfg, now.AddDate(0, 0, -90), now.AddDate(0, 0, 1)); err == nil {
			act.Events = events
		} else {
			act.CalendarErr = err
		}
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
		V: cacheVersion, Gap: cfg.GapMinutes, Env: cacheEnv(cfg),
		Minutes: map[string][]int64{},
		Roots:   map[string]string{},
	}
	st, err := os.Stat(path)
	if err != nil {
		return fc
	}
	f, err := os.Open(path)
	if err != nil {
		// leave the size at zero so the next scan tries this file again
		// instead of trusting an empty result forever
		return fc
	}
	fc.Size, fc.ModTime = st.Size(), st.ModTime().Unix()
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
						fc.Calls = append(fc.Calls, Call{
							ID: id, Day: t.Local().Format("2006-01-02"), Bucket: bucket, T: tk,
						})
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
	// Only an assistant row is an API call. A tool result can echo a
	// subagent's usage back into the parent transcript, and that call is
	// already counted in the subagent's own file. The literal search is
	// deliberate: the row's "type" is serialized after "message", so a keyed
	// lookup would find the message's own type instead.
	if !bytes.Contains(line, []byte(`"type":"assistant"`)) {
		return Tokens{}, "", "", false
	}
	m := bytes.Index(line, []byte(`"message":{`))
	if m < 0 {
		return Tokens{}, "", "", false
	}
	msg := line[m:]
	i := bytes.Index(msg, []byte(`"usage":{`))
	if i < 0 {
		return Tokens{}, "", "", false
	}
	rest := msg[i:]
	tk = Tokens{
		In:        jsonNum(rest, "input_tokens"),
		Out:       jsonNum(rest, "output_tokens"),
		CacheR:    jsonNum(rest, "cache_read_input_tokens"),
		CacheW:    jsonNum(rest, "cache_creation_input_tokens"),
		CacheW1h:  jsonNum(rest, "ephemeral_1h_input_tokens"),
		WebSearch: jsonNum(rest, "web_search_requests"),
		Calls:     1,
	}
	model := jsonField(msg, "model") // from the message, never from tool output
	if model == "" {
		model = "unknown"
	}
	id = jsonField(line, "requestId")
	if id == "" {
		id = jsonField(msg, "id") // message id, when the request id is absent
	}
	return tk, bucketKey(model, jsonField(rest, "speed"), jsonField(rest, "inference_geo")), id, true
}

// fillGaps marks every minute between two events that sit within the idle gap,
// so a session reads as continuous work instead of a dotted line of pings.
// A lone event still counts as one worked minute.
func fillGaps(ts []int64, gap int64) []int64 {
	if !sort.SliceIsSorted(ts, func(i, j int) bool { return ts[i] < ts[j] }) {
		ts = append([]int64(nil), ts...)
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	}
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

// the memo maps are read from the TUI's render path and written from the
// rescan goroutine, so they carry a lock
var (
	gitMemo     sync.Mutex
	topLevels   = map[string]string{}
	remoteNames = map[string]string{}
)

// gitRemoteName is the repo name as the forge knows it, memoized per root.
func gitRemoteName(root string) string {
	gitMemo.Lock()
	v, ok := remoteNames[root]
	gitMemo.Unlock()
	if ok {
		return v
	}
	name := ""
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
		if i := strings.LastIndexAny(url, "/:"); i >= 0 {
			name = url[i+1:]
		}
	}
	gitMemo.Lock()
	remoteNames[root] = name
	gitMemo.Unlock()
	return name
}

// gitTopLevel resolves a path to its repository root (empty if the path is
// gone or untracked). Results are memoized: one exec per distinct cwd.
func gitTopLevel(path string) string {
	gitMemo.Lock()
	v, ok := topLevels[path]
	gitMemo.Unlock()
	if ok {
		return v
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	top := ""
	if err == nil {
		top = strings.TrimSpace(string(out))
	}
	gitMemo.Lock()
	topLevels[path] = top
	gitMemo.Unlock()
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
		// existence outranks any number of votes: a path that is gone cannot
		// answer git questions at all
		score := n
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			score += 1 << 40
		}
		if score > bestScore || (score == bestScore && root < best) {
			best, bestScore = root, score
		}
	}
	return best // a project whose every checkout is gone still names one
}

// cacheEnv fingerprints everything outside the file that shaped its cached
// values: day boundaries come from the local zone, project names from aliases.
func cacheEnv(cfg Config) string {
	zone, offset := time.Now().Zone()
	aliases := make([]string, 0, len(cfg.Aliases))
	for from, to := range cfg.Aliases {
		aliases = append(aliases, from+":"+to)
	}
	sort.Strings(aliases)
	return fmt.Sprintf("%s%d|%s", zone, offset, strings.Join(aliases, ","))
}
