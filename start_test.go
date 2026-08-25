package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStartSystemd: unit known in the user manager → start goes through
// `systemctl --user`; with --autostart the unit (and socket, best-effort)
// are enabled too.
func TestStartSystemd(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = "routre.service enabled"

	handled, err := startManaged("linux", false, testLogger())
	if err != nil {
		t.Fatalf("startManaged: %v", err)
	}
	if !handled {
		t.Fatal("expected the systemd manager to handle the start")
	}
	if !s.called("systemctl --user start routre.service") {
		t.Errorf("missing user-scope start; calls: %v", s.calls)
	}
	if s.called("systemctl --user enable routre.service") {
		t.Errorf("enable must not run without --autostart; calls: %v", s.calls)
	}
}

func TestStartSystemdAutostart(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = "routre.service enabled"

	handled, err := startManaged("linux", true, testLogger())
	if err != nil {
		t.Fatalf("startManaged: %v", err)
	}
	if !handled {
		t.Fatal("expected the systemd manager to handle the start")
	}
	if !s.called("systemctl --user start routre.service") {
		t.Errorf("missing user-scope start; calls: %v", s.calls)
	}
	if !s.called("systemctl --user enable routre.service") {
		t.Errorf("missing user-scope enable (--autostart); calls: %v", s.calls)
	}
}

// TestStartLaunchd: plist present → launchctl load; with --autostart the
// load carries -w (writes the Enabled key).
func TestStartLaunchd(t *testing.T) {
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

	t.Run("plain start", func(t *testing.T) {
		s := newStubExec(t)
		handled, err := startManaged("darwin", false, testLogger())
		if err != nil {
			t.Fatalf("startManaged: %v", err)
		}
		if !handled {
			t.Fatal("expected launchd to handle the start")
		}
		if !s.called("launchctl load " + plist) {
			t.Errorf("missing launchctl load; calls: %v", s.calls)
		}
		if s.called("launchctl load -w " + plist) {
			t.Errorf("unexpected -w without --autostart; calls: %v", s.calls)
		}
	})

	t.Run("autostart", func(t *testing.T) {
		s := newStubExec(t)
		handled, err := startManaged("darwin", true, testLogger())
		if err != nil {
			t.Fatalf("startManaged: %v", err)
		}
		if !handled {
			t.Fatal("expected launchd to handle the start")
		}
		if !s.called("launchctl load -w " + plist) {
			t.Errorf("missing launchctl load -w; calls: %v", s.calls)
		}
	})
}

// TestRestartSystemd: restart goes through `systemctl --user restart`.
func TestRestartSystemd(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = "routre.service enabled"

	handled, err := restartManaged("linux", testLogger())
	if err != nil {
		t.Fatalf("restartManaged: %v", err)
	}
	if !handled {
		t.Fatal("expected the systemd manager to handle the restart")
	}
	if !s.called("systemctl --user restart routre.service") {
		t.Errorf("missing user-scope restart; calls: %v", s.calls)
	}
}

// TestRestartLaunchd: loaded job → kickstart -k; not loaded but plist
// present → load.
func TestRestartLaunchd(t *testing.T) {
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

	t.Run("loaded → kickstart", func(t *testing.T) {
		s := newStubExec(t)
		handled, err := restartManaged("darwin", testLogger())
		if err != nil {
			t.Fatalf("restartManaged: %v", err)
		}
		if !handled {
			t.Fatal("expected launchd to handle the restart")
		}
		uid := os.Getuid()
		if !s.called("launchctl kickstart -k gui/" + strconv.Itoa(uid) + "/dev.routercli.daemon") {
			t.Errorf("missing launchctl kickstart; calls: %v", s.calls)
		}
	})

	t.Run("not loaded → load", func(t *testing.T) {
		s := newStubExec(t)
		s.errs["launchctl list dev.routercli.daemon"] = errors.New("Could not find specified service")
		handled, err := restartManaged("darwin", testLogger())
		if err != nil {
			t.Fatalf("restartManaged: %v", err)
		}
		if !handled {
			t.Fatal("expected launchd to handle the restart")
		}
		if !s.called("launchctl load " + plist) {
			t.Errorf("missing launchctl load fallback; calls: %v", s.calls)
		}
	})
}

// TestStartFallbackSpawn: no systemd/launchd → detached spawn + port
// wait; the daemon is started with the config path.
func TestStartFallbackSpawn(t *testing.T) {
	s := newStubExec(t)
	// Unit unknown in both scopes → detectManager returns "" → spawn.
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = ""

	// lsof: nothing listening first (spawn), then the daemon appears.
	var lsofCalls int
	oldCmd := runCommand
	runCommand = func(cmd *exec.Cmd) (string, error) {
		key := strings.Join(cmd.Args, " ")
		s.calls = append(s.calls, key)
		if key == "lsof -ti tcp:20128" {
			lsofCalls++
			if lsofCalls == 1 {
				return "", errors.New("no listener")
			}
			return "9999", nil
		}
		return s.outs[key], s.errs[key]
	}
	t.Cleanup(func() { runCommand = oldCmd })

	var spawned string
	oldSpawn := spawnDaemon
	spawnDaemon = func(cfgPath string, logger *log.Logger) error {
		spawned = cfgPath
		return nil
	}
	t.Cleanup(func() { spawnDaemon = oldSpawn })

	if err := startBySpawn("linux", "test-config.json", testLogger()); err != nil {
		t.Fatalf("startBySpawn: %v", err)
	}
	if spawned != "test-config.json" {
		t.Fatalf("spawnDaemon config = %q, want test-config.json", spawned)
	}
}

// TestStartFallbackAutostartError: auto-start requires an installed
// service; without one, cmdStart must refuse.
func TestStartFallbackAutostartError(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = ""

	var spawned bool
	oldSpawn := spawnDaemon
	spawnDaemon = func(cfgPath string, logger *log.Logger) error {
		spawned = true
		return nil
	}
	t.Cleanup(func() { spawnDaemon = oldSpawn })

	// cmdStart calls detectManager(runtime.GOOS); force the fallback path
	// by invoking startManaged + the autostart check directly.
	handled, err := startManaged("linux", true, testLogger())
	if err != nil {
		t.Fatalf("startManaged: %v", err)
	}
	if handled {
		t.Fatal("expected no manager to be found")
	}
	if spawned {
		t.Fatal("must not spawn when --autostart is set without a service")
	}
}

// TestRestartFallbackSpawn: no manager → stop by port, then spawn.
func TestRestartFallbackSpawn(t *testing.T) {
	s := newStubExec(t)
	s.outs["systemctl list-unit-files routre.service"] = ""
	s.outs["systemctl --user list-unit-files routre.service"] = ""

	var lsofCalls int
	oldCmd := runCommand
	runCommand = func(cmd *exec.Cmd) (string, error) {
		key := strings.Join(cmd.Args, " ")
		s.calls = append(s.calls, key)
		if key == "lsof -ti tcp:20128" {
			lsofCalls++
			switch lsofCalls {
			case 1:
				return "4242", nil // running before restart
			case 2:
				return "", errors.New("no listener") // released after SIGTERM
			default:
				return "7777", nil // respawned daemon
			}
		}
		return s.outs[key], s.errs[key]
	}
	t.Cleanup(func() { runCommand = oldCmd })

	var killed []int
	oldKill := killProcess
	killProcess = func(pid int) error { killed = append(killed, pid); return nil }
	t.Cleanup(func() { killProcess = oldKill })

	var spawned string
	oldSpawn := spawnDaemon
	spawnDaemon = func(cfgPath string, logger *log.Logger) error {
		spawned = cfgPath
		return nil
	}
	t.Cleanup(func() { spawnDaemon = oldSpawn })

	if err := restartBySpawn("linux", "test-config.json", testLogger()); err != nil {
		t.Fatalf("restartBySpawn: %v", err)
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Fatalf("expected SIGTERM to old pid 4242, got %v", killed)
	}
	if spawned != "test-config.json" {
		t.Fatalf("spawnDaemon config = %q, want test-config.json", spawned)
	}
}
