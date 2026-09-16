package sockdial

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A socket whose absolute path exceeds sun_path must still be dialable. This is
// the macOS failure mode: the sessions dir lives under a deep path, so a plain
// net.DialTimeout("unix", longPath) returns ENAMETOOLONG/EINVAL.
func TestDialLongPath(t *testing.T) {
	// Build a directory deep enough that the socket path is well past the
	// smallest sun_path limit (104 on macOS).
	root := t.TempDir()
	deep := filepath.Join(root, strings.Repeat("d", 40), strings.Repeat("e", 40), strings.Repeat("f", 40))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(deep, "s.sock")
	if len(sock) <= maxDirectLen {
		t.Fatalf("test socket path unexpectedly short (%d bytes); can't exercise the long-path branch", len(sock))
	}

	// Listen via a short symlink so bind() itself isn't blocked by sun_path
	// (that's exe-scroll's job in production; here we only test the dial side).
	// Symlink the *directory*, not the socket file: bind() does not follow a
	// symlink at the final path component and would reject it as EADDRINUSE
	// (Linux), so the last component (s.sock) must not exist yet. The socket
	// still lands at the real long path deep/s.sock.
	shortRoot, err := os.MkdirTemp("/tmp", "sd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(shortRoot)
	dirLink := filepath.Join(shortRoot, "d")
	if err := os.Symlink(deep, dirLink); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dirLink, "s.sock"))
	if err != nil {
		t.Fatalf("listen via short dir symlink: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// The socket file now exists at the long absolute path. A direct dial must
	// fail (proving the hazard is real) while sockdial.Dial must succeed.
	if _, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		t.Skip("platform allows dialing an over-long sun_path directly; nothing to prove here")
	}
	conn, err := Dial(sock, time.Second)
	if err != nil {
		t.Fatalf("sockdial.Dial on long path: %v", err)
	}
	conn.Close()
}

// The common short-path case must behave exactly like net.DialTimeout and not
// create any symlink.
func TestDialShortPath(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	conn, err := Dial(sock, time.Second)
	if err != nil {
		t.Fatalf("Dial short path: %v", err)
	}
	conn.Close()
}

// The per-call link directory must be private (0700) and owned by us, so no
// other non-root user can delete our link and swap in a socket of their own
// between symlink() and connect(). This is what lets us not depend on /tmp
// being sticky.
func TestShortSymlinkDirIsPrivate(t *testing.T) {
	link, cleanup, err := shortSymlink("/nonexistent/target.sock")
	if err != nil {
		t.Fatalf("shortSymlink: %v", err)
	}
	defer cleanup()

	if len(link) > maxDirectLen {
		t.Errorf("link path length %d exceeds sun_path budget %d", len(link), maxDirectLen)
	}

	dir := filepath.Dir(link)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat link dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("link dir mode = %o, want 0700 (group/other must have no access)", perm)
	}

	// The link must be a symlink (not a real file) pointing at exactly our target.
	lst, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link is not a symlink: mode %v", lst.Mode())
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != "/nonexistent/target.sock" {
		t.Errorf("link target = %q, want our supplied target", got)
	}

	// cleanup must remove the whole directory.
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup left dir behind: stat err = %v", err)
	}
}
