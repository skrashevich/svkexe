package db

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestDBPath(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"absolute", "relative", "uri", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "nested", "test.db")
			dsn := path + "?_busy_timeout=1000"
			switch kind {
			case "relative":
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(cwd, path)
				if err != nil {
					t.Fatal(err)
				}
				dsn = rel + "?_busy_timeout=1000"
			case "uri":
				path = filepath.Join(dir, "spaces # and ?.db")
				dsn = (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rwc"}).String()
			case "symlink":
				path = filepath.Join(dir, "test.db")
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dsn = filepath.Join(link, "test.db")
			}
			database, err := New(Config{DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if database.Path() != path {
				t.Fatalf("Path() = %q, want %q (DSN %q)", database.Path(), path, dsn)
			}
			if _, err := os.Stat(database.Path()); err != nil {
				t.Fatal(err)
			}
			other, err := New(Config{DSN: filepath.Join(t.TempDir(), "other.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if database.Path() != path || database.Path() == other.Path() {
				t.Fatal("database path is not instance-local")
			}
		})
	}
}

func TestDBPathRejectsMemoryURI(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{"file::memory:?cache=shared", "file:memorydb?mode=memory&cache=shared"} {
		database, err := New(Config{DSN: dsn})
		if err == nil {
			database.Close()
			t.Fatalf("New(%q) accepted a database without a filesystem path", dsn)
		}
	}
}
