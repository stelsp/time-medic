package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestFillGapsBridgesShortIdleOnly(t *testing.T) {
	// two pings 3 minutes apart are one stretch of work; a 40-minute hole is not
	got := fillGaps([]int64{100, 103, 143}, 15)
	want := []int64{100, 101, 102, 103, 143}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestFillGapsDedupesSameMinute(t *testing.T) {
	if got := fillGaps([]int64{7, 7, 7}, 15); len(got) != 1 {
		t.Fatalf("a busy minute is still one minute: %v", got)
	}
}

func TestBuildWallClockCountsOverlapOnce(t *testing.T) {
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.Local).Unix() / 60
	act := &Activity{
		Minutes: map[string]map[minute]bool{
			"a": {minute(base): true, minute(base + 1): true},
			"b": {minute(base + 1): true, minute(base + 2): true},
		},
		Roots: map[string]string{},
	}
	rep := Build(act, Config{GapMinutes: 15}, Daily(time.Unix(base*60, 0)))
	if rep.TotalMins != 3 {
		t.Fatalf("wall clock = 3 minutes, got %d", rep.TotalMins)
	}
	if rep.SumMins != 4 {
		t.Fatalf("per-project sum = 4 minutes, got %d", rep.SumMins)
	}
}

func TestBuildSplitsSessionsOnIdleGap(t *testing.T) {
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.Local).Unix() / 60
	mins := map[minute]bool{}
	for _, m := range []int64{0, 1, 2, 60, 61} {
		mins[minute(base+m)] = true
	}
	rep := Build(&Activity{Minutes: map[string]map[minute]bool{"a": mins}, Roots: map[string]string{}},
		Config{GapMinutes: 15}, Daily(time.Unix(base*60, 0)))
	if len(rep.Sessions) != 2 {
		t.Fatalf("an hour apart is two sessions, got %d", len(rep.Sessions))
	}
	if rep.Days[0].First.Format("15:04") != "09:00" {
		t.Fatalf("day starts at the first tracked minute, got %s", rep.Days[0].First.Format("15:04"))
	}
}

func TestWeeklyIsMondayToMonday(t *testing.T) {
	p := Weekly(time.Date(2026, 8, 30, 23, 0, 0, 0, time.Local)) // a Sunday
	if p.From.Format("2006-01-02") != "2026-08-24" || p.To.Format("2006-01-02") != "2026-08-31" {
		t.Fatalf("got %s..%s", p.From, p.To)
	}
	if p.Label != "2026-W35" {
		t.Fatalf("label %s", p.Label)
	}
}

func TestSelectorsResolveToTheRightWeek(t *testing.T) {
	now := time.Now()
	if !Weekly(parseWeek("last")).To.Equal(Weekly(now).From) {
		t.Fatal("last week ends where this week starts")
	}
	if parseDay("yesterday").Format("2006-01-02") != now.AddDate(0, 0, -1).Format("2006-01-02") {
		t.Fatal("yesterday is one day back")
	}
	if !Weekly(parseWeek("-2")).From.Equal(Weekly(now).From.AddDate(0, 0, -14)) {
		t.Fatal("-2 is two weeks back")
	}
}

func TestHMAndBar(t *testing.T) {
	if hm(0) != "—" || hm(45) != "45m" || hm(125) != "2h05m" {
		t.Fatalf("hm: %q %q %q", hm(0), hm(45), hm(125))
	}
	if b := bar(1, 100, 10); b != "█░░░░░░░░░" {
		t.Fatalf("a tracked minute always shows: %q", b)
	}
	if b := bar(0, 100, 4); b != "░░░░" {
		t.Fatalf("empty day is empty: %q", b)
	}
}

func TestJSONFieldCutsValueWithoutParsing(t *testing.T) {
	line := []byte(`{"type":"user","cwd":"/tmp/x","timestamp":"2026-08-26T10:00:00.000Z"}`)
	if got := jsonField(line, "cwd"); got != "/tmp/x" {
		t.Fatalf("cwd: %q", got)
	}
	if got := jsonField(line, "timestamp"); got != "2026-08-26T10:00:00.000Z" {
		t.Fatalf("timestamp: %q", got)
	}
	if got := jsonField(line, "missing"); got != "" {
		t.Fatalf("absent field: %q", got)
	}
}

func TestUsageOfTakesMessageLevelCountsOnce(t *testing.T) {
	// the iterations array repeats the same tokens; only the first copy counts
	line := []byte(`{"type":"assistant","requestId":"req_abc","message":{"model":"claude-opus-5","usage":` +
		`{"input_tokens":2,"cache_creation_input_tokens":400,"cache_read_input_tokens":536013,` +
		`"output_tokens":928,"iterations":[{"input_tokens":2,"output_tokens":928}]}}}`)
	tk, bucket, id, ok := usageOf(line)
	if !ok || ModelOf(bucket) != "claude-opus-5" {
		t.Fatalf("bucket %q ok %v", bucket, ok)
	}
	if tk.In != 2 || tk.Out != 928 || tk.CacheR != 536013 || tk.CacheW != 400 || tk.Calls != 1 {
		t.Fatalf("counts: %+v", tk)
	}
	if id != "req_abc" {
		t.Fatalf("the call id is what de-duplicates streamed rows: %q", id)
	}
	if _, _, _, ok := usageOf([]byte(`{"type":"user"}`)); ok {
		t.Fatal("a line without usage is not an AI call")
	}
}

func TestUnattendedSplitsRobotsFromPeople(t *testing.T) {
	if !unattended("sdk-cli") || !unattended("sdk-py") {
		t.Fatal("sdk entrypoints are unattended")
	}
	if unattended("cli") || unattended("claude-desktop") {
		t.Fatal("interactive entrypoints are not")
	}
}

func TestTaskKeyRoundTrip(t *testing.T) {
	proj, branch := splitTask(taskKey("ai-viewer-proto", "feat-116-s2"))
	if proj != "ai-viewer-proto" || branch != "feat-116-s2" {
		t.Fatalf("got %q %q", proj, branch)
	}
	if p, b := splitTask(taskKey("misc", "")); p != "misc" || b != "" {
		t.Fatalf("empty branch survives: %q %q", p, b)
	}
}

func TestTaskLabelAndState(t *testing.T) {
	ts := TaskStat{Branch: "hotspot-ai", Ref: "#83", Commits: []Commit{{}}}
	if ts.Label() != "hotspot-ai #83" {
		t.Fatalf("label %q", ts.Label())
	}
	if ts.State() != "open" {
		t.Fatalf("state %q", ts.State())
	}
	if (TaskStat{Branch: "feat-116-s2", Ref: "#116"}).Label() != "feat-116-s2" {
		t.Fatal("a branch that already names the issue is not repeated")
	}
	if (TaskStat{Branch: "main"}).State() != "trunk" {
		t.Fatal("trunk work is not a task")
	}
	if (TaskStat{Branch: "old", Gone: true}).State() != "gone" {
		t.Fatal("a deleted branch reads as gone")
	}
}

func TestTaskRefPrefersBranchThenSubject(t *testing.T) {
	if got := taskRef("feat-116-s2", nil); got != "#116" {
		t.Fatalf("branch ref: %q", got)
	}
	if got := taskRef("hotspot-ai", []Commit{{Subject: "feat(mrtop): hotspots (#83)"}}); got != "#83" {
		t.Fatalf("subject ref: %q", got)
	}
	if got := taskRef("main", []Commit{{Subject: "fix: thing (#12)"}}); got != "" {
		t.Fatalf("trunk carries no task: %q", got)
	}
}

func TestPunchcardBucketsByWeekdayAndHour(t *testing.T) {
	when := time.Date(2026, 8, 26, 14, 30, 0, 0, time.Local) // a Wednesday
	m := minute(when.Unix() / 60)
	act := &Activity{Minutes: map[string]map[minute]bool{"a": {m: true, m + 1: true}}}
	card := Punchcard(act, when.AddDate(0, 0, -1), when.AddDate(0, 0, 1))
	if card[2][14] != 2 {
		t.Fatalf("Wednesday 14:00 should hold 2 minutes, got %d", card[2][14])
	}
}

func TestWindowMarksWhatIsCut(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	got := window(lines, 0, 3)
	if !strings.Contains(got, "2 more below") || strings.Contains(got, "above") {
		t.Fatalf("top of list: %q", got)
	}
	if got := window(lines, 2, 3); !strings.Contains(got, "more above") {
		t.Fatalf("scrolled: %q", got)
	}
	if got := window(lines[:2], 0, 5); got != "a\nb" {
		t.Fatalf("short list is untouched: %q", got)
	}
}

func TestCompactKeepsHeadersOnOneLine(t *testing.T) {
	if compact(950) != "950" || compact(12_400) != "12k" || compact(1_572_000_000) != "1572.0M" {
		t.Fatalf("%s %s %s", compact(950), compact(12_400), compact(1_572_000_000))
	}
}

func TestBuildTasksFoldsBranchesOfOneIssue(t *testing.T) {
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.Local).Unix() / 60
	set := func(offsets ...int64) map[minute]bool {
		m := map[minute]bool{}
		for _, o := range offsets {
			m[minute(base+o)] = true
		}
		return m
	}
	act := &Activity{
		Minutes: map[string]map[minute]bool{"proj": set(0, 1, 2, 3)},
		Tasks: map[string]map[minute]bool{
			taskKey("proj", "feat-116"):    set(0, 1),
			taskKey("proj", "feat-116-s2"): set(1, 2), // overlaps by one minute
			taskKey("proj", "chore-x"):     set(3),
		},
		Roots: map[string]string{},
	}
	rep := Build(act, Config{GapMinutes: 15}, Daily(time.Unix(base*60, 0)))
	if len(rep.Tasks) != 2 {
		t.Fatalf("two tasks expected (#116 and chore-x), got %d: %+v", len(rep.Tasks), rep.Tasks)
	}
	issue := rep.Tasks[0]
	if issue.Ref != "#116" || len(issue.Branches) != 2 {
		t.Fatalf("branches did not fold: %+v", issue)
	}
	if issue.Mins != 3 {
		t.Fatalf("the shared minute is counted once: %d", issue.Mins)
	}
	if !strings.HasPrefix(issue.Label(), "#116 (2 branches)") {
		t.Fatalf("label %q", issue.Label())
	}
}

func TestSlackifyBoldsHeadings(t *testing.T) {
	got := slackify("# WEEK 2026-W35\n\n## DAYS\nMon 24  5h\n")
	if !strings.Contains(got, "*WEEK 2026-W35*") || !strings.Contains(got, "*DAYS*") {
		t.Fatalf("headings not converted: %q", got)
	}
	if !strings.Contains(got, "Mon 24  5h") {
		t.Fatalf("body lost: %q", got)
	}
}

func TestWriteOutCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "weekly.md")
	got, err := writeOut(path, "hello")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read back %q err %v", data, err)
	}
}

func TestPostSlackRefusesWithoutAWebhook(t *testing.T) {
	if err := postSlack("", "report"); err == nil {
		t.Fatal("no webhook must be an error, not a silent no-op")
	}
}

func TestPriceTableCostAppliesTheRulesTheCLIBillsBy(t *testing.T) {
	pt := PriceTable{Source: "test", rates: map[string]Rates{
		"claude-opus-5": {In: 5, Out: 25, CacheWrite: 6.25, CacheW1h: 10, CacheRead: 0.5, WebSearch: 0.01},
	}}
	// one million of each, with half the cache writes on the 1h TTL
	tk := Tokens{In: 1_000_000, Out: 1_000_000, CacheR: 1_000_000,
		CacheW: 1_000_000, CacheW1h: 500_000, WebSearch: 3}
	got, ok := pt.Cost(bucketKey("claude-opus-5", "standard", "not_available"), tk)
	if !ok {
		t.Fatal("the model is in the table")
	}
	want := 5.0 + 25 + 0.5 + 0.5*10 + 0.5*6.25 + 3*0.01
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("got %f want %f", got, want)
	}

	// US-pinned inference carries a surcharge on tokens, never on web search
	usGot, _ := pt.Cost(bucketKey("claude-opus-5", "standard", "us"), tk)
	usWant := (want-3*0.01)*usGeoSurcharge + 3*0.01
	if diff := usGot - usWant; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("us geo: got %f want %f", usGot, usWant)
	}

	// fast mode is billed at its own rates, above the model's usual tier
	fastGot, _ := pt.Cost(bucketKey("claude-opus-5", "fast", ""), Tokens{Out: 1_000_000})
	if fastGot != 50 {
		t.Fatalf("fast output should cost $50/Mtok, got %f", fastGot)
	}

	if _, ok := pt.Cost(bucketKey("some-other-model", "", ""), tk); ok {
		t.Fatal("an unpriced model must not silently cost zero")
	}
}

func TestPriceTablePrefixFallbackFindsDatedModels(t *testing.T) {
	pt := PriceTable{rates: map[string]Rates{"claude-opus-5": {Out: 25}}}
	got, ok := pt.Cost(bucketKey("claude-opus-5-20260114", "", ""), Tokens{Out: 1_000_000})
	if !ok || got != 25 {
		t.Fatalf("dated ids should fall back to their family: %f %v", got, ok)
	}
}

func TestParseCatalogRefusesAPartialTable(t *testing.T) {
	good := []byte(`schema_version:1,pricing_tiers:{tier_5_25:{input:5,output:25,cache_write_5m:6.25,` +
		`cache_write_1h:10,cache_read:0.5,web_search:0.01}},models:[{id:"claude-opus-5",family:"opus",` +
		`display_name:"Opus 5",context:{window:1e6},pricing:"tier_5_25"}]`)
	rates, err := parseCatalog(good)
	if err != nil || rates["claude-opus-5"].Out != 25 {
		t.Fatalf("rates %+v err %v", rates, err)
	}
	if _, err := parseCatalog([]byte(`schema_version:1,pricing_tiers:{}`)); err == nil {
		t.Fatal("a table with no tiers must be an error, not empty prices")
	}
	if _, err := parseCatalog([]byte(`schema_version:1,pricing_tiers:{tier_5_25:{input:5,output:25,` +
		`cache_write_5m:6.25,cache_write_1h:10,cache_read:0.5,web_search:0.01}}`)); err == nil {
		t.Fatal("tiers with no model must be an error")
	}
}

func TestVersionLessOrdersNumerically(t *testing.T) {
	if !versionLess("/x/2.1.9", "/x/2.1.10") {
		t.Fatal("2.1.9 comes before 2.1.10")
	}
	if versionLess("/x/2.2.0", "/x/2.1.99") {
		t.Fatal("2.2.0 is newer than 2.1.99")
	}
}

func TestJSONFloatReadsDecimalsAndScientific(t *testing.T) {
	line := []byte(`{"type":"cost-state","totalCostUSD":0.1943,"other":1e-5}`)
	if v, ok := jsonFloat(line, "totalCostUSD"); !ok || v != 0.1943 {
		t.Fatalf("got %v %v", v, ok)
	}
	if v, ok := jsonFloat(line, "other"); !ok || v != 1e-5 {
		t.Fatalf("scientific: %v %v", v, ok)
	}
	if _, ok := jsonFloat(line, "missing"); ok {
		t.Fatal("absent key must not read as zero")
	}
}

func TestUsageOfIgnoresEchoedToolResults(t *testing.T) {
	// a tool result can carry a subagent's usage payload; that call belongs to
	// the subagent's own transcript and must not be billed twice here
	echo := []byte(`{"type":"user","toolUseResult":{"usage":{"input_tokens":10,"output_tokens":20},` +
		`"model":"claude-opus-5"}}`)
	if _, _, _, ok := usageOf(echo); ok {
		t.Fatal("only an assistant row is an API call")
	}
	real := []byte(`{"type":"assistant","requestId":"req_1","message":{"model":"claude-fable-5",` +
		`"usage":{"input_tokens":10,"output_tokens":20}}}`)
	tk, bucket, id, ok := usageOf(real)
	if !ok || ModelOf(bucket) != "claude-fable-5" || id != "req_1" || tk.Out != 20 {
		t.Fatalf("assistant row: %+v %q %q %v", tk, bucket, id, ok)
	}
}

func TestDayBoundariesSurviveAZoneThatSkipsMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago") // clocks jump 00:00 → 01:00
	if err != nil {
		t.Skip("zone database unavailable")
	}
	transition := time.Date(2026, 9, 6, 15, 0, 0, 0, loc)
	start := dayStart(transition)
	if start.Day() != 6 || start.Month() != time.September {
		t.Fatalf("day start fell into the previous day: %s", start)
	}
	if next := nextDay(start); next.Day() != 7 {
		t.Fatalf("next day: %s", next)
	}
	w := Weekly(transition)
	days := 0
	for d := w.From; d.Before(w.To); d = nextDay(d) {
		days++
	}
	if days != 7 {
		t.Fatalf("a week has seven days even across a transition, got %d", days)
	}
}

func TestTaskRefIgnoresDatesAndVersions(t *testing.T) {
	for _, branch := range []string{"release-2026-08", "v1-2-3", "cleanup-2024"} {
		if got := taskRef(branch, nil); got != "" {
			t.Fatalf("%q should name no issue, got %q", branch, got)
		}
	}
	for branch, want := range map[string]string{
		"feat-116": "#116", "fix/2043": "#2043", "bugfix_7": "#7", "feature-116-s2": "#116",
	} {
		if got := taskRef(branch, nil); got != want {
			t.Fatalf("%q: got %q want %q", branch, got, want)
		}
	}
}

func TestAnsiTruncKeepsEscapesIntact(t *testing.T) {
	colored := "\x1b[38;5;214mamber\x1b[0m tail"
	got := ansiTrunc(colored, 5)
	if !strings.HasPrefix(got, "\x1b[38;5;214m") {
		t.Fatalf("color start lost: %q", got)
	}
	if strings.Contains(got, "tail") {
		t.Fatalf("cut past the width: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("must close the color it opened: %q", got)
	}
}

func TestFitFrameNeverExceedsTheTerminal(t *testing.T) {
	frame := strings.Join([]string{strings.Repeat("x", 50), "short", strings.Repeat("y", 90)}, "\n")
	got := fitFrame(frame, 20, 2)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("height not clipped: %d lines", len(lines))
	}
	for _, ln := range lines {
		if lipgloss.Width(ln) > 20 {
			t.Fatalf("line wider than the terminal: %q", ln)
		}
	}
}

func TestBestRootPrefersACheckoutThatStillExists(t *testing.T) {
	live := t.TempDir()
	if got := bestRoot(map[string]int{"/gone/for/good": 500, live: 1}); got != live {
		t.Fatalf("existence outranks votes, got %q", got)
	}
	if got := bestRoot(map[string]int{"/gone/a": 1, "/gone/b": 2}); got != "/gone/b" {
		t.Fatalf("with every checkout gone the most-seen one still names it, got %q", got)
	}
}

func TestUnpricedModelIsReportedNotSilentlyFree(t *testing.T) {
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.Local)
	day := base.Format("2006-01-02")
	act := &Activity{
		Minutes: map[string]map[minute]bool{"p": {minute(base.Unix() / 60): true}},
		Tasks:   map[string]map[minute]bool{},
		Roots:   map[string]string{},
		Tokens: map[string]map[string]*Tokens{day: {
			bucketKey("brand-new-model", "standard", ""): {Out: 2_000_000, Calls: 1},
		}},
	}
	rep := Build(act, Config{GapMinutes: 15}, Daily(base))
	if rep.CostTotal != 0 {
		t.Fatalf("an unknown model must not be priced: %f", rep.CostTotal)
	}
	if len(rep.Unpriced) != 1 || rep.Unpriced[0] != "brand-new-model" {
		t.Fatalf("unpriced models must be named: %v", rep.Unpriced)
	}
}

func TestKeyboardMinutesJoinTheWallClockWithoutDoubleCounting(t *testing.T) {
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.Local).Unix() / 60
	set := func(offsets ...int64) map[minute]bool {
		m := map[minute]bool{}
		for _, o := range offsets {
			m[minute(base+o)] = true
		}
		return m
	}
	act := &Activity{
		Minutes: map[string]map[minute]bool{"proj": set(0, 1)},
		Tasks:   map[string]map[minute]bool{},
		Roots:   map[string]string{},
		// one minute overlaps the session, two are outside it
		Apps: map[string]map[minute]bool{"Figma": set(1, 2, 3)},
	}
	rep := Build(act, Config{GapMinutes: 15}, Daily(time.Unix(base*60, 0)))
	if rep.SessionMins != 2 {
		t.Fatalf("session minutes: %d", rep.SessionMins)
	}
	if rep.AppTotal != 3 || rep.AppMins["Figma"] != 3 {
		t.Fatalf("keyboard minutes: %d %v", rep.AppTotal, rep.AppMins)
	}
	if rep.TotalMins != 4 {
		t.Fatalf("wall clock counts the overlap once: %d", rep.TotalMins)
	}
	if line := rep.SourceLine(); !strings.Contains(line, "2m in Claude Code") ||
		!strings.Contains(line, "1m elsewhere") {
		t.Fatalf("source line: %q", line)
	}
}

func TestMeetingsCountEvenWithNobodyTyping(t *testing.T) {
	start := time.Date(2026, 8, 26, 14, 0, 0, 0, time.Local)
	act := &Activity{
		Minutes: map[string]map[minute]bool{},
		Tasks:   map[string]map[minute]bool{},
		Roots:   map[string]string{},
		Events: []Event{
			{Start: start, End: start.Add(30 * time.Minute), Calendar: "Work", Busy: true, People: 4},
			{Start: start, End: start.Add(24 * time.Hour), Calendar: "Work", Busy: true, AllDay: true},
			{Start: start, End: start.Add(5 * time.Minute), Calendar: "Work", Busy: true},
			{Start: start.Add(time.Hour), End: start.Add(2 * time.Hour), Calendar: "Work", Busy: false},
		},
	}
	rep := Build(act, Config{GapMinutes: 15, MeetingMinutes: 10}, Daily(start))
	if len(rep.Meetings) != 1 {
		t.Fatalf("all-day markers, free time and five-minute slots are not meetings: %d", len(rep.Meetings))
	}
	if rep.MeetingMins != 30 || rep.TotalMins != 30 {
		t.Fatalf("meeting minutes %d, wall clock %d", rep.MeetingMins, rep.TotalMins)
	}
}

func TestSamplesBecomeMinutesOnlyWhileSomebodyIsThere(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{StateDir: dir, GapMinutes: 15, IdleSeconds: 180}
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.Local).Unix()
	lines := fmt.Sprintf("%d|0|Ghostty\n%d|12|Ghostty\n%d|900|Ghostty\n%d|3|Figma\n",
		base, base+60, base+120, base+180)
	if err := os.WriteFile(cfg.SamplePath(), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	apps, err := ReadSamples(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps["Ghostty"]) != 2 {
		t.Fatalf("the idle sample must not count: %d", len(apps["Ghostty"]))
	}
	if len(apps["Figma"]) != 1 {
		t.Fatalf("Figma: %d", len(apps["Figma"]))
	}
}

func TestReadSamplesWithoutASensorIsNotAnError(t *testing.T) {
	apps, err := ReadSamples(Config{StateDir: t.TempDir()})
	if err != nil || apps != nil {
		t.Fatalf("a machine with no sensor simply has no samples: %v %v", apps, err)
	}
}
