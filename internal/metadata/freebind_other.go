//go:build !linux

package metadata

import "syscall"

// freebind does nothing off Linux. IP_FREEBIND is a Linux socket option and the
// platform that runs the VMs is Linux; this exists so the package builds and its
// tests run on a developer's machine.
func freebind(_, _ string, _ syscall.RawConn) error { return nil }
