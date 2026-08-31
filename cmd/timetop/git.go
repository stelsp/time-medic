// Artifacts: what actually landed in the tracked minutes. Time alone is a
// number; a weekly needs the commits that came out of it.
package main

import (
	"os/exec"
	"regexp"
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

// mainRef is the integration branch a task branch is measured against.
func mainRef(root string) string {
	if v, ok := mainRefs[root]; ok {
		return v
	}
	ref := ""
	for _, cand := range []string{"origin/main", "origin/master", "main", "master"} {
		if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", cand).Run() == nil {
			ref = cand
			break
		}
	}
	mainRefs[root] = ref
	return ref
}

var mainRefs = map[string]string{}

// branchExists guards every branch-scoped query: worktrees come and go.
func branchExists(root, branch string) bool {
	return branch != "" &&
		exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", branch).Run() == nil
}

// branchMerged reports whether the branch already landed on the integration
// branch — the difference between "done" and "done, waiting".
func branchMerged(root, branch string) bool {
	ref := mainRef(root)
	if ref == "" || !branchExists(root, branch) {
		return false
	}
	out, err := exec.Command("git", "-C", root, "merge-base", "--is-ancestor", branch, ref).CombinedOutput()
	return err == nil && len(out) == 0
}

// commitsOnBranch returns the user's commits made on a branch inside the
// period. An unmerged branch is asked for its own commits only (^main), so a
// task shows its own work and not the trunk it was cut from.
func commitsOnBranch(root, branch, author string, from, to time.Time) []Commit {
	if !branchExists(root, branch) {
		// the branch was merged and deleted: its work still exists somewhere,
		// findable by the issue it referenced
		if ref := taskRef(branch, nil); ref != "" {
			return commitsByRef(root, ref, author, from, to)
		}
		return nil
	}
	rev := []string{branch}
	if ref := mainRef(root); ref != "" && !branchMerged(root, branch) {
		rev = append(rev, "^"+ref)
	}
	args := append([]string{"-C", root, "log", "--no-merges",
		"--since=" + from.Format(time.RFC3339), "--until=" + to.Format(time.RFC3339),
		"--pretty=%H%x1f%at%x1f%s"}, rev...)
	if author != "" {
		args = append(args, "--author="+author)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	var cs []Commit
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(ln, "\x1f")
		if len(f) != 3 {
			continue
		}
		sec, _ := strconv.ParseInt(f[1], 10, 64)
		cs = append(cs, Commit{SHA: f[0][:7], When: time.Unix(sec, 0), Subject: f[2], Branch: branch})
	}
	return cs
}

// trunk branches carry no task of their own: work recorded on them is trunk
// work, not a numbered task.
func isTrunk(branch string) bool {
	switch branch {
	case "main", "master", "dev", "develop", "trunk", "HEAD":
		return true
	}
	return false
}

// taskRef pulls the issue reference out of a branch name or commit subject —
// "feat-116-s2" and "fix(api): … #116" are the same task.
func taskRef(branch string, commits []Commit) string {
	if isTrunk(branch) {
		return ""
	}
	if m := refPattern.FindStringSubmatch(branch); m != nil {
		return "#" + m[1]
	}
	for _, c := range commits {
		if m := hashRef.FindStringSubmatch(c.Subject); m != nil {
			return "#" + m[1]
		}
	}
	return ""
}

var (
	refPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{1,6})(?:-|$)`)
	hashRef    = regexp.MustCompile(`#([0-9]{1,6})`)
)

// commitsByRef finds a deleted branch's work by the issue it named.
func commitsByRef(root, ref, author string, from, to time.Time) []Commit {
	args := []string{"-C", root, "log", "--all", "--no-merges",
		"--since=" + from.Format(time.RFC3339), "--until=" + to.Format(time.RFC3339),
		"--fixed-strings", "--grep=" + ref, "--pretty=%H%x1f%at%x1f%s"}
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
