package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
