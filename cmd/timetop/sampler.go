// The keyboard sensor. Claude Code proves work at a project; this proves the
// simpler thing it cannot see — that a human was at this machine at all, and
// what they had in front of them. It asks the OS two questions no permission
// dialog guards: how long since the last input, and which app is in front.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Sample is one observation of the machine, written as a line per interval.
type Sample struct {
	When time.Time
	Idle time.Duration
	App  string
}

// sampleInterval is how often the sensor looks. Every 30 seconds is enough to
// resolve a worked minute and cheap enough to forget it is running.
const sampleInterval = 30 * time.Second

// SamplePath is the append-only log the sensor writes and the reports read.
func (c Config) SamplePath() string { return filepath.Join(c.StateDir, "samples.log") }

// Watch runs the sensor until it is told to stop. It appends one line per
// interval and nothing else: no titles, no keystrokes, no window contents.
func Watch(cfg Config) error {
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return err
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	tick := time.NewTicker(sampleInterval)
	defer tick.Stop()
	writeSample(cfg, takeSample())
	for {
		select {
		case <-tick.C:
			writeSample(cfg, takeSample())
		case <-stop:
			return nil
		}
	}
}

// takeSample asks the OS the two questions that need no permission.
func takeSample() Sample {
	return Sample{When: time.Now(), Idle: idleTime(), App: frontApp()}
}

func writeSample(cfg Config, s Sample) {
	f, err := os.OpenFile(cfg.SamplePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%d|%d|%s\n", s.When.Unix(), int(s.Idle.Seconds()), s.App)
}

var idleRe = regexp.MustCompile(`"HIDIdleTime" = (\d+)`)

// idleTime is how long since the last keypress or mouse move, from the HID
// system. A long idle means the machine was awake but nobody was.
func idleTime() time.Duration {
	out, err := exec.Command("ioreg", "-c", "IOHIDSystem", "-d", "1").Output()
	if err != nil {
		return 0
	}
	m := idleRe.FindSubmatch(out)
	if m == nil {
		return 0
	}
	ns, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return 0
	}
	return time.Duration(ns)
}

var frontAppRe = regexp.MustCompile(`"LSDisplayName"\s*=\s*"([^"]+)"`)

// frontApp is the application in front, by name. lsappinfo answers without the
// accessibility permission that scripting the window server would need.
func frontApp() string {
	asn, err := exec.Command("lsappinfo", "front").Output()
	if err != nil {
		return ""
	}
	info, err := exec.Command("lsappinfo", "info", "-only", "name", strings.TrimSpace(string(asn))).Output()
	if err != nil {
		return ""
	}
	if m := frontAppRe.FindSubmatch(info); m != nil {
		return string(m[1])
	}
	return ""
}

// ReadSamples turns the sensor log into worked minutes per app. A sample
// counts when the machine had input within the idle threshold; the minutes
// between two close samples count too, the same way transcripts are filled.
func ReadSamples(cfg Config) (map[string]map[minute]bool, error) {
	f, err := os.Open(cfg.SamplePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // the sensor was never started; that is not an error
		}
		return nil, err
	}
	defer f.Close()

	byApp := map[string][]int64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		if len(parts) < 3 {
			continue
		}
		ts, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		idle, err := strconv.Atoi(parts[1])
		if err != nil || idle > cfg.IdleSeconds {
			continue // awake machine, absent human
		}
		app := parts[2]
		if app == "" {
			app = "unknown app"
		}
		byApp[app] = append(byApp[app], ts/60)
	}
	out := map[string]map[minute]bool{}
	for app, mins := range byApp {
		// two samples a minute apart are one stretch at the keyboard; a longer
		// hole is a break, exactly as with transcripts
		set := map[minute]bool{}
		for _, m := range fillGaps(mins, int64(cfg.GapMinutes)) {
			set[minute(m)] = true
		}
		out[app] = set
	}
	return out, nil
}
