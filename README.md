<div align="center">

`✦ time-medic`

**Time tracking that runs itself.** No timer to start, no timer to forget.
Your work already leaves a trail — this reads it and writes your daily and
weekly for you.

</div>

---

## What it does

`timetop` reconstructs your working day from evidence you already leave behind.
Nothing is self-reported, so nothing depends on you remembering to press start.

Three sensors, each answering a different question:

| sensor | what it proves | permission |
|--------|----------------|------------|
| **Claude Code transcripts** (`~/.claude/projects/**/*.jsonl`) | work on a known project and branch, plus what it cost in tokens | none |
| **the keyboard sensor** (`timetop watch`) | a human was at this machine, and which app they had in front | none |
| **the calendar** (EventKit) | meetings — work that leaves no keystroke | one click, once |

Minutes between two nearby signals count as worked minutes; a hole longer than
the idle gap is a break. The three are unioned, so an hour spent in a call
while a session is open is still one hour. Commits from the same repositories
are attached on top, so a report says both how long you worked and what came
out of it.

### The keyboard sensor

```
timetop watch --install     start it, and keep it starting at login
timetop watch --status      is it running?
timetop watch --stop        stop it and remove the login agent
```

It writes one line every 30 seconds to `~/.config/timetop/state/samples.log`,
mode `0600`:

```
1788254095|0|Ghostty
```

A timestamp, seconds since your last input, and the name of the app in front.
No window titles, no keystrokes, no screen contents, and nothing leaves the
machine. Stopping it leaves the samples it already wrote; delete the file to
forget them.

### Meetings

Set `CALENDAR=1` and run `timetop calendar` once from your own terminal: macOS
asks for calendar access, you click Allow, and from then on meetings count as
worked time. Google, iCloud and Exchange calendars all arrive the same way, as
long as the account is in the Calendar app.

Only the shape of an event is read — start, end, calendar name, account, head
count, and the *domains* of the people in it — never their addresses, and never
titles unless you set `CALENDAR_TITLES=1`. Events you declined, all-day markers
and anything marked free are not counted.

**Work or personal** is decided without reading a single title:

```
timetop calendar --calendars     # what you have, and who you meet there
```

```
CALENDAR          ACCOUNT   EVENTS      TIME  WHO YOU MEET
work@company.com  Google        41    22h30m  company.com, client.com
personal@gmail…   Google         6     4h10m  gmail.com
```

Name the work ones and the rest is left out of your totals:

```
WORK_CALENDARS=work@company.com
WORK_DOMAINS=company.com
```

A calendar named in `WORK_CALENDARS` is work; one in `PERSONAL_CALENDARS` never
is; otherwise an event counts as work when someone from a `WORK_DOMAINS` domain
is in it. Personal events still appear in the report — as a count and a total
that were deliberately left out — so you can see the classifier's work rather
than trust it. `COUNT_PERSONAL=1` counts them too.

**A call you code through is one hour, not two.** Meeting minutes are unioned
with everything else, and every meeting says what else was running during it:

```
Tue 01  13:00–13:45      45m  work                           30m worked
Thu 03  19:00–20:30    1h30m  work                           listening
2h15m in meetings, 1h45m of it with nothing else running
```

That difference is the interesting number: an hour of undivided attention and
an hour of half-listening are not the same hour, and only the calendar plus the
transcripts together can tell them apart.

```
WEEK 2026-W35 · Mon Aug 24 – Sun Aug 30

14h05m · 3 active days · 21 sessions · avg 4h41m/day · 134 commits
16h56m across projects (2h51m of it in parallel sessions)

DAYS
Mon 24     ░░░░░░░░░░░░░░        —
Tue 25     ░░░░░░░░░░░░░░        —  5 commits
Wed 26     ████████████░░    5h52m  12:53–20:13  38 commits  ai-viewer-proto 5h48m
Thu 27     ██████████████    6h47m  12:44–23:59  73 commits  merge-medic 5h33m
Fri 28     ██░░░░░░░░░░░░    1h26m  00:00–17:18  18 commits  merge-medic 1h22m
```

## Install

```
git clone https://github.com/stelsp/time-medic
cd time-medic
make install        # builds bin/timetop and copies it to ~/.local/bin
```

## Use

```
timetop                     dashboard: three screens over the same minutes
timetop weekly last         last week's report
timetop weekly --md         markdown, ready to paste into a standup
timetop tasks last          one line per branch: time, state, commits
timetop daily yesterday     one day, session by session — projects, tasks, sessions
timetop scan                all-time totals per project
timetop prices              the price table, read out of the claude CLI itself
timetop prices --check      replay sessions and compare with Claude's own totals

timetop weekly --out ~/standup.md      write the report to a file
timetop weekly --slack                 post it to SLACK_WEBHOOK from the config
```

### Screens

| key | screen | what it answers |
|-----|--------|-----------------|
| `1` | WEEK | how long, which days, which projects, what shipped |
| `2` | TASKS | which branch each hour went into, whether it landed |
| `3` | RHYTHM | when you actually work — weekday × hour over 8 weeks |

The header carries a live marker while a session is running (`● project 35m`),
the weekly gauge against your target, and the tab bar.

Selectors: `today` / `yesterday` / `YYYY-MM-DD` / `-N` for days, and
`this` / `last` / `YYYY-MM-DD` / `-N` for weeks.

Dashboard keys: `↑↓` day, `tab` panel, `←→` week, `t` today, `y` copy the
weekly as markdown, `Y` copy the selected day, `r` rescan, `q` quit.

## How the numbers are built

- **Worked minute** — a minute covered by a transcript entry, or lying between
  two entries no further apart than `GAP_MINUTES` (default 15).
- **Session** — a run of worked minutes on one project, ended by a longer gap.
- **Wall clock vs. project sum** — two projects worked in the same minute cost
  you one minute of life; the header reports the union, and the difference is
  shown as parallel work.
- **Project** — the git remote name, so a worktree, a duty clone and the dev
  checkout of one repository report as one project.
- **Task** — the branch recorded on each entry, so hours land on `feat-116`
  even when the work happened in the main checkout. Branches that carry the
  same issue fold into one task (`#116 (6 branches)`), with their minutes
  unioned and their commits deduplicated. State comes from git:
  `merged`, `open`, `gone` (merged and deleted), `trunk`, `quiet` (no commits).
  A deleted branch is still asked for its commits by the issue it named.
- **Unattended time** — sessions started by a script (`sdk-*` entrypoints,
  e.g. a bot calling `claude -p`) are counted and reported separately, so your
  robots' hours never pass as yours.
- **Tokens** — read off the same transcripts per model and per day: calls,
  output, input, cache reads. Streamed rows of one API call are folded back
  together by `requestId`, so nothing is counted twice.
- **Dollars** — priced with the catalog the installed `claude` CLI carries
  inside itself: the same tiers it bills you with, including the 1h vs 5m
  cache-write split, the US-inference surcharge and fast-mode rates. Upgrade
  the CLI and the prices follow. Nothing is hardcoded, nothing is fetched.
  Streamed rows are folded by `requestId` across every file, so a resumed or
  forked session is not billed twice. `timetop prices --check` replays the
  sessions where Claude Code recorded its own total and prints the drift
  (−3.7% here: calls Claude Code billed but wrote no assistant row for). A
  model missing from the table is reported as unpriced, with its token volume,
  never as free.
- **Coverage** — reports say when the oldest transcript starts. Days before it
  read as `no data`, not as an idle `—`, and days with commits but no session
  log are named explicitly.

The scan is cached under `~/.config/timetop/state/` — one file per cache
format, keyed by transcript size, mtime, the idle gap, your timezone and your
aliases — so a rescan of an unchanged transcript costs nothing and a changed
setting is never served from a stale answer.

## Sharing a report

`--out PATH` writes the rendered report to a file (directories are created).
`--slack` posts it to the incoming webhook in `SLACK_WEBHOOK`; both flags imply
markdown. Nothing is ever posted unless you type `--slack` — the tool has no
background publishing.

## Configuration

Optional, `~/.config/timetop/config.env` — see
[`config.example.env`](config.example.env).

## License

MIT
