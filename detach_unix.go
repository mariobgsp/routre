//go:build !windows

package main

import "syscall"

// detachAttr returns process attributes that detach the daemon from the
// controlling terminal: a new session with no controlling tty, so the
// daemon survives the launching shell and keeps running in the background.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
