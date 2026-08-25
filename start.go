// routre start / restart: start or restart the gateway daemon.
//
//	routre start   [--autostart] [-config config.json]
//	routre restart [-config config.json]
//
// When the daemon is installed as an OS service it is started through the
// service manager (systemd unit in the system or --user scope, or launchd
// agent, see deploy/); otherwise `start` spawns a detached `serve`
// background process writing to ~/.routre/daemon.log and waits for
// the configured port to come up.
//
// With --autostart, boot/login auto-start is enabled as well
// (systemctl enable / launchctl load -w); auto-start requires an
// installed service. `restart` keeps the current auto-start state.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// cmdStart starts the gateway daemon; with autostart it also enables
// auto-start at boot/login.
func cmdStart(cfgPath string, autostart bool, logger *log.Logger) error {
	handled, err := startManaged(runtime.GOOS, autostart, logger)
	if err != nil {
		return err
	}
	if !handled {
		if autostart {
			return fmt.Errorf("no systemd/launchd service found — auto-start requires an installed service (see deploy/ or `make install`)")
		}
		return startBySpawn(runtime.GOOS, cfgPath, logger)
	}
	return nil
}

// cmdRestart restarts the gateway daemon, keeping the auto-start state.
func cmdRestart(cfgPath string, logger *log.Logger) error {
	handled, err := restartManaged(runtime.GOOS, logger)
	if err != nil {
		return err
	}
	if !handled {
		return restartBySpawn(runtime.GOOS, cfgPath, logger)
	}
	return nil
}

// startManaged starts the daemon through its OS service manager and
// reports whether one was found (false → caller should spawn).
func startManaged(goos string, autostart bool, logger *log.Logger) (bool, error) {
	switch detectManager(goos) {
	case "systemd":
		scope := systemdKnownScope()
		if scope == "" {
			return false, nil
		}
		sctl := func(args ...string) *exec.Cmd { return systemctlCmd(scope, args...) }
		if err := runQuiet(sctl("start", systemdUnit)); err != nil {
			return false, fmt.Errorf("systemctl start %s: %v", systemdUnit, err)
		}
		logger.Printf("started %s (%s scope)", systemdUnit, scope)
		if autostart {
			if err := runQuiet(sctl("enable", systemdUnit)); err != nil {
				return false, fmt.Errorf("systemctl enable %s: %v", systemdUnit, err)
			}
			// The socket unit may not exist; best-effort.
			if err := runQuiet(sctl("enable", systemdSocket)); err != nil {
				logger.Printf("note: socket %s not enabled (%v)", systemdSocket, err)
			}
			logger.Printf("auto-start enabled (%s)", systemdUnit)
		}
		return true, nil

	case "launchd":
		plist := findLaunchdPlist()
		if plist == "" {
			return false, nil
		}
		if autostart {
			// load -w also writes the Enabled key: auto-start at login.
			if err := runQuiet(exec.Command("launchctl", "load", "-w", plist)); err != nil {
				return false, fmt.Errorf("launchctl load -w %s: %v", plist, err)
			}
			logger.Printf("started %s, auto-start enabled", launchdLabel)
		} else {
			if err := runQuiet(exec.Command("launchctl", "load", plist)); err != nil {
				return false, fmt.Errorf("launchctl load %s: %v", plist, err)
			}
			logger.Printf("started %s", launchdLabel)
		}
		return true, nil
	}
	return false, nil
}

// restartManaged restarts the daemon through its OS service manager and
// reports whether one was found (false → caller should spawn).
func restartManaged(goos string, logger *log.Logger) (bool, error) {
	switch detectManager(goos) {
	case "systemd":
		scope := systemdKnownScope()
		if scope == "" {
			return false, nil
		}
		sctl := func(args ...string) *exec.Cmd { return systemctlCmd(scope, args...) }
		if err := runQuiet(sctl("restart", systemdUnit)); err != nil {
			return false, fmt.Errorf("systemctl restart %s: %v", systemdUnit, err)
		}
		logger.Printf("restarted %s (%s scope)", systemdUnit, scope)
		return true, nil

	case "launchd":
		plist := findLaunchdPlist()
		if plist == "" {
			return false, nil
		}
		if launchdLoaded() {
			// kickstart -k kills and restarts the job.
			domain := fmt.Sprintf("gui/%d", os.Getuid())
			if err := runQuiet(exec.Command("launchctl", "kickstart", "-k", domain+"/"+launchdLabel)); err != nil {
				return false, fmt.Errorf("launchctl kickstart: %v", err)
			}
			logger.Printf("restarted %s", launchdLabel)
		} else {
			if err := runQuiet(exec.Command("launchctl", "load", plist)); err != nil {
				return false, fmt.Errorf("launchctl load %s: %v", plist, err)
			}
			logger.Printf("started %s", launchdLabel)
		}
		return true, nil
	}
	return false, nil
}

// spawnDaemon starts a detached `serve` process writing to the daemon
// log. Swappable in tests; the spawned pid is logged here and discovered
// again by waitForPort, so callers only need the error.
var spawnDaemon = func(cfgPath string, logger *log.Logger) error {
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %v", err)
	}
	logPath := daemonLogPath()
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log %s: %v", logPath, err)
	}
	defer f.Close()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	attr := &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{devnull, f, f},
		Sys:   detachAttr(),
	}
	proc, err := os.StartProcess(bin, []string{bin, "serve", "-config", cfgPath}, attr)
	if err != nil {
		return fmt.Errorf("start daemon: %v", err)
	}
	logger.Printf("daemon spawned (pid %d, log %s)", proc.Pid, logPath)
	return nil
}

// startBySpawn starts the daemon as a detached background process when no
// OS service is installed.
func startBySpawn(goos, cfgPath string, logger *log.Logger) error {
	port := listenPort(cfgPath)
	pid, err := pidOnPort(goos, port)
	if err != nil {
		return err
	}
	if pid > 0 {
		logger.Printf("gateway already running (pid %d on :%d)", pid, port)
		return nil
	}
	if err := spawnDaemon(cfgPath, logger); err != nil {
		return err
	}
	if err := waitForPort(goos, port, logger); err != nil {
		return fmt.Errorf("start: %v", err)
	}
	return nil
}

// restartBySpawn stops whatever listens on the port, then spawns a fresh
// detached daemon.
func restartBySpawn(goos, cfgPath string, logger *log.Logger) error {
	if err := stopByPort(goos, cfgPath, logger); err != nil {
		return err
	}
	if err := spawnDaemon(cfgPath, logger); err != nil {
		return err
	}
	if err := waitForPort(goos, listenPort(cfgPath), logger); err != nil {
		return fmt.Errorf("restart: %v", err)
	}
	return nil
}

// waitForPort polls until something listens on port (the daemon is up).
func waitForPort(goos string, port int, logger *log.Logger) error {
	deadline := time.Now().Add(10 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		pid, err := pidOnPort(goos, port)
		if err != nil {
			return err
		}
		if pid > 0 {
			logger.Printf("gateway up (pid %d on :%d)", pid, port)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon did not start listening on :%d within 10s (check %s)", port, daemonLogPath())
		}
		<-tick.C
	}
}

// daemonLogPath returns the fallback daemon log location. The path is
// derived only from ROUTRE_CLI_DATA_DIR (or the user's home dir) plus a
// fixed file name — never from request or CLI input — so it is a trusted
// location; filepath.Clean keeps it tidy.
func daemonLogPath() string {
	if dir := os.Getenv("ROUTRE_CLI_DATA_DIR"); dir != "" {
		return filepath.Clean(filepath.Join(dir, "daemon.log"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "routre-daemon.log"
	}
	return filepath.Clean(filepath.Join(home, ".routre", "daemon.log"))
}
