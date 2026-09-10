//go:build unix

package updater

import "syscall"

// detachedSysProcAttr puts the update command in its own process group so a
// signal delivered to the gateway (systemd stopping it as part of the update)
// does not reach the updater.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
