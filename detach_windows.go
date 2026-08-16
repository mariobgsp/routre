//go:build windows

package main

import "syscall"

const (
	detachedProcess       = 0x00000008 // DETACHED_PROCESS
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
)

// detachAttr detaches the daemon from the console on Windows: it gets no
// console window and its own process group, so it survives the launching
// shell.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
