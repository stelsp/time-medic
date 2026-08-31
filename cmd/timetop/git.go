// Artifacts: what actually landed in the tracked minutes. Time alone is a
// number; a weekly needs the commits that came out of it.
package main

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// commitsIn returns the user's commits in a repo within [from, to).
func commitsIn(root, author string, from, to time.Time) []Commit {
	args := []string{"-C", root, "log", "--all", "--no-merges",
		"--since=" + from.Format(time.RFC3339), "--until=" + to.Format(time.RFC3339),
		"--pretty=%H%x1f%at%x1f%s"}
	if author != "" {
		args = append(args, "--author="+author)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var cs []Commit
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(ln, "\x1f")
		if len(f) != 3 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		sec, _ := strconv.ParseInt(f[1], 10, 64)
		cs = append(cs, Commit{SHA: f[0][:7], When: time.Unix(sec, 0), Subject: f[2]})
	}
	return cs
}

// gitAuthor falls back to the repo's configured identity when the config file
// does not pin one, so "my commits" means mine out of the box.
func gitAuthor(root string) string {
	out, err := exec.Command("git", "-C", root, "config", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
