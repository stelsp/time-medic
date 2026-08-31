// Getting a report out of the terminal: to a file, or to the channel where
// the standup actually happens.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// writeOut saves a report next to wherever the user wants it, creating the
// directory on the way. Returns the path it actually wrote.
func writeOut(path, content string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	return abs, nil
}

// postSlack sends the report to the configured incoming webhook. It runs only
// when the flag is typed: nothing about a report is published on its own.
func postSlack(webhook, text string) error {
	if webhook == "" {
		return fmt.Errorf("no SLACK_WEBHOOK in %s — add one to post from here",
			filepath.Join(configDir(), "config.env"))
	}
	body, err := json.Marshal(map[string]string{"text": slackify(text)})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack answered %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	return nil
}

// slackify converts the markdown we already produce into Slack's mrkdwn:
// headings become bold lines, everything else survives as it is.
func slackify(md string) string {
	var b strings.Builder
	for _, ln := range strings.Split(md, "\n") {
		if body := strings.TrimLeft(ln, "#"); len(body) < len(ln) {
			ln = "*" + strings.TrimSpace(body) + "*"
		}
		b.WriteString(ln + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
