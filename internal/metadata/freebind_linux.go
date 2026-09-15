//go:build linux

package metadata

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// freebind lets the socket bind an address the host does not have yet. It is the
// difference between "the gateway must start after Incus has brought the VM
// bridge up" and "the order does not matter": with it the listener is open from
// the start, and packets begin arriving the moment the bridge address exists.
//
// The cost is that a genuinely wrong address binds just as happily as an absent
// one, which is why Start logs when the address is not local — see
// warnIfAddressIsNotLocal.
//
// The option needs no privilege — it only relaxes the bind check, it does not
// claim the address — so it works under the service's unprivileged user.
func freebind(_, _ string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_FREEBIND, 1)
	}); err != nil {
		return err
	}
	return setErr
}
