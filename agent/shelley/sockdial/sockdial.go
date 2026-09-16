// Package sockdial dials Unix-domain sockets whose path may be longer than the
// kernel's sun_path limit.
//
// A sockaddr_un's path field is a small fixed array (104 bytes on macOS, 108 on
// Linux) and the kernel copies the *literal* string we pass to connect(2) --- it
// never resolves or normalizes the path first. So a perfectly valid, existing
// socket whose absolute path is long (which happens routinely on macOS, where
// per-user data lives under ~/Library/... or /var/folders/...) fails to connect
// with ENAMETOOLONG/EINVAL even though every path-based syscall that touches the
// file works fine.
//
// Dial sidesteps that by connecting through a short-lived symlink placed in a
// short directory: the kernel resolves the (short) symlink path via normal path
// resolution --- which is only bounded by PATH_MAX --- to the same socket inode.
// Unlike a chdir-based workaround this is safe in a multi-goroutine server: each
// call uses its own uniquely named symlink and never mutates process-wide state.
package sockdial

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"time"
)

// maxDirectLen is the longest socket path we hand straight to the kernel. It is
// deliberately below the smallest sun_path limit (104 on macOS) so the common
// short-path case is byte-for-byte the old behavior and only genuinely long
// paths take the symlink detour. The value leaves room for the trailing NUL.
const maxDirectLen = 100

// Dial connects to the Unix socket at path, transparently handling paths too
// long for sun_path. timeout bounds the whole operation.
func Dial(path string, timeout time.Duration) (net.Conn, error) {
	if len(path) <= maxDirectLen {
		return net.DialTimeout("unix", path, timeout)
	}
	link, cleanup, err := shortSymlink(path)
	if err != nil {
		// Best effort: fall back to a direct dial so the caller still gets a
		// real connection (or a real, explanatory error) rather than an error
		// about our workaround.
		return net.DialTimeout("unix", path, timeout)
	}
	defer cleanup()
	return net.DialTimeout("unix", link, timeout)
}

// shortRoot returns a short parent directory in which to place the private
// per-call link directory. $TMPDIR is preferred when it is itself short (as on
// Linux), otherwise /tmp -- which resolves to the 11-byte /private/tmp on macOS
// and is short on Linux too. macOS's per-user $TMPDIR is >100 bytes, which is
// exactly the length we're routing around, so it can't be used there.
func shortRoot() string {
	if d := os.Getenv("TMPDIR"); d != "" && len(d) <= 20 {
		return d
	}
	return "/tmp"
}

// shortSymlink creates a uniquely named symlink to target and returns its path;
// the caller removes it (and its parent dir) via the returned cleanup. The link
// path is guaranteed short enough to fit in sun_path.
//
// Security: the link is created inside a fresh 0700 directory we own (via
// os.MkdirTemp, which mkdir(2)s a random name -- it never follows or reuses a
// pre-existing entry). Because only we (and root) can write in that directory,
// no other non-root user can delete our link and swap in their own between the
// symlink() and the connect() -- so we never rely on /tmp being sticky to
// prevent a replace-the-link TOCTOU. The symlink itself grants nothing: dialing
// still requires write permission on the *target* socket, which exe-scroll
// creates 0600 in a 0700 sessions directory, so a symlink can't widen access to
// it. We point the link at our own socket, so we are never following an
// attacker-controlled link.
func shortSymlink(target string) (link string, cleanup func(), err error) {
	dir, err := os.MkdirTemp(shortRoot(), ".shelley-sock-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		cleanup()
		return "", nil, err
	}
	link = filepath.Join(dir, hex.EncodeToString(b[:])+".sock")
	if err := os.Symlink(target, link); err != nil {
		cleanup()
		return "", nil, err
	}
	return link, cleanup, nil
}
