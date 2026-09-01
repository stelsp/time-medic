// Running the sensor without being asked to. One launchd agent, started at
// login, restarted if it dies — the same shape merge-medic's watcher uses.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const daemonLabel = "com.time-medic.sampler"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", daemonLabel+".plist")
}

// InstallDaemon writes and loads the login agent that keeps the sensor awake.
func InstallDaemon(cfg Config) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the sensor is macOS-only for now — run `timetop watch` yourself elsewhere")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>watch</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonLabel, self, filepath.Join(cfg.StateDir, "sampler.err"))
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	// bootout first so a reinstall replaces the running job instead of failing
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid+"/"+daemonLabel).Run()
	if out, err := exec.Command("launchctl", "bootstrap", uid, plistPath()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// RemoveDaemon stops the sensor and forgets it. The samples it already wrote
// stay where they are; deleting those is the user's call, not ours.
func RemoveDaemon() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid+"/"+daemonLabel).Run()
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DaemonLoaded reports whether launchd currently holds the sensor.
func DaemonLoaded() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	return exec.Command("launchctl", "print", uid+"/"+daemonLabel).Run() == nil
}
