//go:build !unix

package updater

import "syscall"

// detachedSysProcAttr has no portable equivalent outside unix; the gateway is
// only deployed on Linux, so non-unix builds start the command attached.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return nil
}
