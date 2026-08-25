package main

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubExec replaces the command-runner seams with a recorder so tests can
// assert exactly which commands would be executed without running them.
// Each call is recorded as "name arg1 arg2". Behavior comes from `out`
// (output for runCommand) and `err` maps keyed by that same string.
type stubExec struct {
	calls  []string
	outs   map[string]string
	errs   map[string]error
	oldCmd func(*exec.Cmd) (string, error)
	oldQ   func(*exec.Cmd) error
	oldLp  func(string) (string, error)
	oldKp  func(int) error
}

func newStubExec(t *testing.T) *stubExec {
	t.Helper()
	s := &stubExec{
		outs:   map[string]string{},
		errs:   map[string]error{},
		oldCmd: runCommand,
		oldQ:   runQuiet,
		oldLp:  lookPath,
		oldKp:  killProcess,
	}
	runCommand = func(cmd *exec.Cmd) (string, error) {
		// cmd.Path is the absolute resolved path; key on cmd.Args[0] so
		// keys read as the user would type them ("lsof -ti …").
		key := strings.Join(cmd.Args, " ")
		s.calls = append(s.calls, key)
		if err := s.errs[key]; err != nil {
			return s.outs[key], err
		}
		return s.outs[key], nil
	}
	runQuiet = func(cmd *exec.Cmd) error {
		key := strings.Join(cmd.Args, " ")
		s.calls = append(s.calls, key)
		return s.errs[key]
	}
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	killProcess = func(pid int) error { return nil }
	t.Cleanup(func() {
		runCommand = s.oldCmd
		runQuiet = s.oldQ
		lookPath = s.oldLp
		killProcess = s.oldKp
	})
	return s
}

func (s *stubExec) called(key string) bool {
	for _, c := range s.calls {
		if c == key {
			return true
		}
	}
	return false
}

// testLogger returns a logger that writes nowhere (test output stays clean).
func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// TestStopSystemdUserScope: unit known + active in the user manager →
// stop/disable go through `systemctl --user`.
func TestStopSystemdUserScope(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = "routre.service enabled"
	s.outs["systemctl --user is-active routre.service"] = "active"
	s.outs["systemctl --user is-active routre.socket"] = "inactive"

	stopped, err := stopManaged("linux", true, testLogger())
	if err != nil {
		t.Fatalf("stopManaged: %v", err)
	}
	if !stopped {
		t.Fatal("expected the systemd manager to handle the stop")
	}
	if !s.called("systemctl --user stop routre.service") {
		t.Errorf("missing user-scope stop; calls: %v", s.calls)
	}
	if !s.called("systemctl --user disable routre.service") {
		t.Errorf("missing user-scope disable (-autostart); calls: %v", s.calls)
	}
	if s.called("systemctl stop routre.service") {
		t.Errorf("must not touch the system scope; calls: %v", s.calls)
	}
}

// TestStopSystemdSystemScope: active in the system manager → stop without
// --user, no disable without -autostart.
func TestStopSystemdSystemScope(t *testing.T) {
	s := newStubExec(t)
	// System scope knows the unit and it is active there; the user scope
	// does not know it → stop must go through the system manager.
	s.outs["systemctl list-unit-files routre.service"] = "routre.service enabled"
	s.outs["systemctl is-active routre.service"] = "active"
	s.outs["systemctl --user list-unit-files routre.service"] = ""

	stopped, err := stopManaged("linux", false, testLogger())
	if err != nil {
		t.Fatalf("stopManaged: %v", err)
	}
	if !stopped {
		t.Fatal("expected the systemd manager to handle the stop")
	}
	if !s.called("systemctl stop routre.service") {
		t.Errorf("missing system-scope stop; calls: %v", s.calls)
	}
	if s.called("systemctl disable routre.service") {
		t.Errorf("disable must not run without -autostart; calls: %v", s.calls)
	}
}

// TestStopSystemdInactive: unit known but inactive in both managers →
// falls back to the port scan (stopManaged reports false).
func TestStopSystemdInactive(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl is-active routre.service"] = "inactive"
	s.errs["systemctl is-active routre.service"] = errors.New("inactive")
	s.outs["systemctl --user is-active routre.service"] = "inactive"
	s.errs["systemctl --user is-active routre.service"] = errors.New("inactive")

	stopped, err := stopManaged("linux", false, testLogger())
	if err != nil {
		t.Fatalf("stopManaged: %v", err)
	}
	if stopped {
		t.Fatal("expected fallback (not active anywhere)")
	}
}

// TestStopLaunchd: plist present + job loaded → launchctl unload; with
// -autostart the unload carries -w (writes the Disabled key).
func TestStopLaunchd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(agents, "dev.routercli.daemon.plist")
	if err := os.WriteFile(plist, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("plain stop", func(t *testing.T) {
		s := newStubExec(t) // launchctl present via lookPath stub
		stopped, err := stopManaged("darwin", false, testLogger())
		if err != nil {
			t.Fatalf("stopManaged: %v", err)
		}
		if !stopped {
			t.Fatal("expected launchd to handle the stop")
		}
		if !s.called("launchctl unload " + plist) {
			t.Errorf("missing launchctl unload; calls: %v", s.calls)
		}
		if s.called("launchctl unload -w " + plist) {
			t.Errorf("unexpected -w without -autostart; calls: %v", s.calls)
		}
	})

	t.Run("autostart", func(t *testing.T) {
		s := newStubExec(t)
		stopped, err := stopManaged("darwin", true, testLogger())
		if err != nil {
			t.Fatalf("stopManaged: %v", err)
		}
		if !stopped {
			t.Fatal("expected launchd to handle the stop")
		}
		if !s.called("launchctl unload -w " + plist) {
			t.Errorf("missing launchctl unload -w; calls: %v", s.calls)
		}
	})
}

// TestStopLaunchdNotLoaded: job not loaded → fall back to port scan.
func TestStopLaunchdNotLoaded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(agents, "dev.routercli.daemon.plist")
	if err := os.WriteFile(plist, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStubExec(t)
	s.errs["launchctl list dev.routercli.daemon"] = errors.New("Could not find specified service")

	stopped, err := stopManaged("darwin", false, testLogger())
	if err != nil {
		t.Fatalf("stopManaged: %v", err)
	}
	if stopped {
		t.Fatal("expected fallback when the job is not loaded")
	}
}

// TestStopByPort: nothing managed → find PID on the port, SIGTERM, wait
// for release.
func TestStopByPort(t *testing.T) {
	s := newStubExec(t)
	first := true
	old := runCommand
	runCommand = func(cmd *exec.Cmd) (string, error) {
		key := strings.Join(cmd.Args, " ")
		s.calls = append(s.calls, key)
		if key == "lsof -ti tcp:20128" {
			if first {
				first = false
				return "4242", nil
			}
			return "", errors.New("no listener")
		}
		return s.outs[key], s.errs[key]
	}
	t.Cleanup(func() { runCommand = old })

	var killed []int
	oldKill := killProcess
	killProcess = func(pid int) error { killed = append(killed, pid); return nil }
	t.Cleanup(func() { killProcess = oldKill })

	if err := stopByPort("linux", "nonexistent-config.json", testLogger()); err != nil {
		t.Fatalf("stopByPort: %v", err)
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Fatalf("expected SIGTERM to pid 4242, got %v", killed)
	}
}

// TestStopByPortNotRunning: no listener on the port → no error, no kill.
func TestStopByPortNotRunning(t *testing.T) {
	s := newStubExec(t)
	s.errs["lsof -ti tcp:20128"] = errors.New("no listener")

	var killed []int
	oldKill := killProcess
	killProcess = func(pid int) error { killed = append(killed, pid); return nil }
	t.Cleanup(func() { killProcess = oldKill })

	if err := stopByPort("linux", "nonexistent-config.json", testLogger()); err != nil {
		t.Fatalf("stopByPort: %v", err)
	}
	if len(killed) != 0 {
		t.Fatalf("must not kill when nothing is listening, killed %v", killed)
	}
}

// TestListenPort: parses the port from the config listen address; falls
// back to the default when the config is missing or malformed.
func TestListenPort(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"listen":"127.0.0.1:20333"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := listenPort(cfg); got != 20333 {
		t.Fatalf("listenPort = %d, want 20333", got)
	}
	if got := listenPort(filepath.Join(dir, "missing.json")); got != defaultPort {
		t.Fatalf("listenPort(missing) = %d, want %d", got, defaultPort)
	}
}

// TestPidFromNetstat: picks the LISTENING socket's PID for the port.
func TestPidFromNetstat(t *testing.T) {
	out := "  TCP    127.0.0.1:20128    127.0.0.1:54321    ESTABLISHED    4242\n" +
		"  TCP    127.0.0.1:20128    0.0.0.0:0    LISTENING    7777\n" +
		"  TCP    127.0.0.1:9999    0.0.0.0:0    LISTENING    1111\n"
	if got := pidFromNetstat(out, 20128); got != 7777 {
		t.Fatalf("pidFromNetstat = %d, want 7777", got)
	}
}
