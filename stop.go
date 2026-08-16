// routre-cli stop: stop the running gateway daemon.
//
//	routre-cli stop [--autostart] [-config config.json]
//
// When the daemon was installed as an OS service it is stopped through the
// service manager (systemd unit in the system or --user scope, or launchd
// agent, see deploy/); otherwise `stop` falls back to finding the process
// listening on the configured port and sending it SIGTERM (the daemon's
// graceful-shutdown signal).
//
// With --autostart, boot/login auto-start is disabled as well
// (systemctl disable / launchctl unload -w).
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"routre-cli/internal/config"
)

// Daemon service identifiers (mirror deploy/).
const (
	systemdUnit   = "routre-cli.service"
	systemdSocket = "routre-cli.socket"
	launchdLabel  = "dev.routercli.daemon"
	defaultPort   = 20128
)

// launchdPlistNames: the install name from the README plus the name the
// deploy/ file currently ships under; either is accepted.
var launchdPlistNames = []string{"dev.routercli.daemon.plist", "dev.routrecli.daemon.plist"}

// runCommand runs cmd and returns combined stdout+stderr. Swappable in
// tests. Command binaries are always compile-time constants at the call
// sites (systemctl/launchctl/lsof/pgrep/netstat/taskkill); the only
// dynamic values are integer PIDs/ports, so no user input can reach a
// shell.
var runCommand = func(cmd *exec.Cmd) (string, error) {
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runQuiet runs cmd discarding all output; only the exit error matters.
// Swappable in tests. (See runCommand for the safety note.)
var runQuiet = func(cmd *exec.Cmd) error {
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// lookPath resolves an executable on PATH. Swappable in tests.
var lookPath = exec.LookPath

// haveExec reports whether an executable with the given name is on PATH.
func haveExec(name string) bool {
	path, err := lookPath(name)
	return err == nil && path != ""
}

// killProcess sends SIGTERM to pid (taskkill on Windows). Swappable in
// tests; on Unix it is the daemon's graceful-shutdown signal (usage is
// saved before exit).
var killProcess = func(pid int) error {
	if runtime.GOOS == "windows" {
		out, err := runCommand(exec.Command("taskkill", "/PID", strconv.Itoa(pid)))
		if err != nil {
			return fmt.Errorf("taskkill: %v (%s)", err, out)
		}
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// cmdStop stops the gateway daemon; with autostart it also disables
// auto-start at boot/login.
func cmdStop(cfgPath string, autostart bool, logger *log.Logger) error {
	stopped, err := stopManaged(runtime.GOOS, autostart, logger)
	if err != nil {
		return err
	}
	if !stopped {
		if autostart {
			logger.Printf("no active systemd/launchd service found — nothing to disable for auto-start")
		}
		return stopByPort(runtime.GOOS, cfgPath, logger)
	}
	return nil
}

// stopManaged stops the daemon through its OS service manager and reports
// whether it was actually running there (false → caller should fall back
// to a port scan).
func stopManaged(goos string, autostart bool, logger *log.Logger) (bool, error) {
	switch detectManager(goos) {
	case "systemd":
		scope := activeSystemdScope()
		if scope == "" {
			logger.Printf("unit %s installed but not active — falling back to port scan", systemdUnit)
			return false, nil
		}
		sctl := func(args ...string) *exec.Cmd { return systemctlCmd(scope, args...) }
		if err := runQuiet(sctl("stop", systemdUnit)); err != nil {
			return false, fmt.Errorf("systemctl stop %s: %v", systemdUnit, err)
		}
		logger.Printf("stopped %s (%s scope)", systemdUnit, scope)
		// Stop the socket too: with socket activation the next connection
		// would otherwise respawn the daemon immediately.
		if out, err := runCommand(sctl("is-active", systemdSocket)); err == nil && strings.TrimSpace(out) == "active" {
			if err := runQuiet(sctl("stop", systemdSocket)); err != nil {
				logger.Printf("note: failed to stop %s: %v", systemdSocket, err)
			} else {
				logger.Printf("stopped %s", systemdSocket)
			}
		}
		if autostart {
			if err := runQuiet(sctl("disable", systemdUnit)); err != nil {
				return false, fmt.Errorf("systemctl disable %s: %v", systemdUnit, err)
			}
			// The socket unit may not exist or not be enabled; best-effort.
			if err := runQuiet(sctl("disable", systemdSocket)); err != nil {
				logger.Printf("note: socket %s not disabled (%v)", systemdSocket, err)
			}
			logger.Printf("auto-start disabled (%s)", systemdUnit)
		}
		return true, nil

	case "launchd":
		if !launchdLoaded() {
			logger.Printf("launchd job %s installed but not loaded — falling back to port scan", launchdLabel)
			return false, nil
		}
		plist := findLaunchdPlist()
		if autostart {
			// unload -w also writes the Disabled key: no auto-start at login.
			if err := runQuiet(exec.Command("launchctl", "unload", "-w", plist)); err != nil {
				return false, fmt.Errorf("launchctl unload -w %s: %v", plist, err)
			}
			logger.Printf("stopped %s, auto-start disabled", launchdLabel)
		} else {
			if err := runQuiet(exec.Command("launchctl", "unload", plist)); err != nil {
				return false, fmt.Errorf("launchctl unload %s: %v", plist, err)
			}
			logger.Printf("stopped %s", launchdLabel)
		}
		return true, nil
	}
	return false, nil
}

// systemctlCmd builds a systemctl invocation for the given scope
// ("system" or "user"); the binary name is fixed, the scope only changes
// arguments.
func systemctlCmd(scope string, args ...string) *exec.Cmd {
	if scope == "user" {
		return exec.Command("systemctl", append([]string{"--user"}, args...)...)
	}
	return exec.Command("systemctl", args...)
}

// systemdUnitKnown reports whether the unit exists in the system or user
// manager (systemctl list-unit-files succeeds for either).
func systemdUnitKnown() bool {
	return systemdKnownScope() != ""
}

// systemdKnownScope returns the scope ("system" or "user") in which the
// unit is installed, or "" if neither manager knows it.
func systemdKnownScope() string {
	for _, scope := range []string{"system", "user"} {
		if out, err := runCommand(systemctlCmd(scope, "list-unit-files", systemdUnit)); err == nil && strings.Contains(out, systemdUnit) {
			return scope
		}
	}
	return ""
}

// activeSystemdScope returns "system" or "user" for the manager in which
// the unit is currently active, else "".
func activeSystemdScope() string {
	for _, scope := range []string{"system", "user"} {
		if out, err := runCommand(systemctlCmd(scope, "is-active", systemdUnit)); err == nil && strings.TrimSpace(out) == "active" {
			return scope
		}
	}
	return ""
}

// detectManager reports the OS service manager the daemon was installed
// under: "systemd" (Linux, unit present in system or user manager) or
// "launchd" (macOS, plist present), else "".
func detectManager(goos string) string {
	switch goos {
	case "linux":
		if !haveExec("systemctl") {
			return ""
		}
		if systemdUnitKnown() {
			return "systemd"
		}
	case "darwin":
		if !haveExec("launchctl") {
			return ""
		}
		if findLaunchdPlist() != "" {
			return "launchd"
		}
	}
	return ""
}

// launchdLoaded reports whether the launchd job is currently loaded
// (launchctl list <label> succeeds only for loaded jobs).
func launchdLoaded() bool {
	return runQuiet(exec.Command("launchctl", "list", launchdLabel)) == nil
}

// findLaunchdPlist returns the path of the installed launchd plist in
// ~/Library/LaunchAgents, or "" when absent.
func findLaunchdPlist() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	for _, n := range launchdPlistNames {
		p := filepath.Join(dir, n)
		fi, err := os.Stat(p)
		if err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// stopByPort finds the process listening on the configured port and stops
// it with SIGTERM (the graceful-shutdown signal), waiting for the port to
// be released.
func stopByPort(goos, cfgPath string, logger *log.Logger) error {
	port := listenPort(cfgPath)
	pid, err := pidOnPort(goos, port)
	if err != nil {
		return err
	}
	if pid == 0 {
		logger.Printf("gateway is not running (nothing listening on :%d)", port)
		return nil
	}
	if err := killProcess(pid); err != nil {
		return fmt.Errorf("stop pid %d: %v", pid, err)
	}
	logger.Printf("stop signal sent to pid %d", pid)
	deadline := time.Now().Add(10 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		if p, err := pidOnPort(goos, port); err != nil || p == 0 {
			logger.Printf("gateway stopped (port :%d released)", port)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pid %d still listening on :%d after 10s (SIGTERM ignored?)", pid, port)
		}
		<-tick.C
	}
}

// pidOnPort returns the PID of the process listening on port, or 0 when
// nothing is (lsof on Unix, netstat on Windows; pgrep fallback when lsof
// is unavailable).
func pidOnPort(goos string, port int) (int, error) {
	if goos == "windows" {
		out, err := runCommand(exec.Command("netstat", "-ano"))
		if err != nil {
			return 0, fmt.Errorf("netstat: %v", err)
		}
		return pidFromNetstat(out, port), nil
	}
	if haveExec("lsof") {
		out, err := runCommand(exec.Command("lsof", "-ti", "tcp:"+strconv.Itoa(port)))
		if err != nil || strings.TrimSpace(out) == "" {
			return 0, nil // no listener (lsof exits non-zero on no match)
		}
		pid, perr := strconv.Atoi(strings.Fields(out)[0])
		if perr != nil {
			return 0, nil
		}
		return pid, nil
	}
	// No lsof: fall back to matching the serve process by command line.
	out, err := runCommand(exec.Command("pgrep", "-f", "routre-cli serve"))
	if err != nil || strings.TrimSpace(out) == "" {
		return 0, nil
	}
	pid, perr := strconv.Atoi(strings.Fields(out)[0])
	if perr != nil {
		return 0, nil
	}
	return pid, nil
}

// pidFromNetstat extracts the PID of the LISTENING socket on port from
// `netstat -ano` output.
func pidFromNetstat(out string, port int) int {
	target := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, target) || !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if pid, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
			return pid
		}
	}
	return 0
}

// listenPort returns the port the daemon listens on, from the config's
// listen address (default: 20128 when the config is unreadable).
func listenPort(cfgPath string) int {
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		return defaultPort
	}
	listen := st.Get().Listen
	i := strings.LastIndex(listen, ":")
	if i < 0 || i == len(listen)-1 {
		return defaultPort
	}
	port, err := strconv.Atoi(listen[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return defaultPort
	}
	return port
}
