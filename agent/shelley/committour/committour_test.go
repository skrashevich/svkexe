package committour

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{
			"-C", dir,
			"-c", "core.hooksPath=/dev/null",
			"-c", "user.name=Test",
			"-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false",
		}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("config", "core.hooksPath", "/dev/null")
	return dir, git
}

func write(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, git func(...string) string, message string) string {
	t.Helper()
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", message, "-m", "Prompt: committour test fixture")
	return strings.TrimSpace(git("rev-parse", "HEAD"))
}

func numbered(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func tourFor(fragments []string) *Tour {
	tour := &Tour{Version: 1, Title: "Tour"}
	for _, fragment := range fragments {
		tour.Chunks = append(tour.Chunks, TourChunk{Patch: fragment, Comment: "explanation"})
	}
	return tour
}

func requireVerify(t *testing.T, dir, commit string, tour *Tour) {
	t.Helper()
	warnings, err := Verify(dir, commit, tour)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Verify warnings=%v err=%v", warnings, err)
	}
}

func TestChunksRoundTripAndMutations(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte(numbered(1, 40)))
	commit(t, git, "base")
	content := strings.Replace(numbered(1, 40), "line 3\n", "line 3 changed\n", 1)
	content = strings.Replace(content, "line 30\n", "line 30 changed\n", 1)
	write(t, dir, "f.txt", []byte(content))
	hash := commit(t, git, "change")

	full, fragments, err := Chunks(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if full != hash || len(fragments) != 2 {
		t.Fatalf("Chunks = %q, %d fragments", full, len(fragments))
	}
	for _, fragment := range fragments {
		if !strings.HasPrefix(fragment, "diff --git a/f.txt b/f.txt\n") || !strings.Contains(fragment, "\n@@") {
			t.Fatalf("bad fragment:\n%s", fragment)
		}
	}

	indexBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	tour := tourFor(fragments)
	requireVerify(t, dir, hash, tour)
	indexAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("Verify changed the real index")
	}

	mutations := map[string]func(*Tour){
		"drop": func(tour *Tour) { tour.Chunks = tour.Chunks[1:] },
		"duplicate": func(tour *Tour) {
			tour.Chunks = append(tour.Chunks, tour.Chunks[0])
		},
		"edit": func(tour *Tour) {
			tour.Chunks[0].Patch = strings.Replace(tour.Chunks[0].Patch, "+line 3 changed", "+line 3 mutated", 1)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := tourFor(fragments)
			mutate(mutated)
			if _, err := Verify(dir, hash, mutated); err == nil {
				t.Fatal("Verify succeeded")
			}
		})
	}
}

func TestVerifySlicesWithDriftedZeroContextHeaders(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte("one\ntwo\nthree\nfour\nfive\n"))
	commit(t, git, "base")
	write(t, dir, "f.txt", []byte("one\nTWO\ninsert\nthree\nFOUR\nfive\n"))
	hash := commit(t, git, "change")
	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 {
		t.Fatalf("fragments = %d, want 1", len(fragments))
	}
	at := strings.Index(fragments[0], "@@")
	if at < 0 {
		t.Fatalf("no hunk in %q", fragments[0])
	}
	header := fragments[0][:at]
	tour := &Tour{Version: 1, Title: "Slices", Chunks: []TourChunk{
		// This slice is applied first even though its new-side line number
		// assumes the insertion from the following slice already exists.
		{Patch: header + "@@ -4 +5 @@\n-four\n+FOUR\n", Comment: "last"},
		{Header: "## First change"},
		{Patch: header + "@@ -2 +2,2 @@\n-two\n+TWO\n+insert\n", Comment: "first"},
	}}
	requireVerify(t, dir, hash, tour)
}

func TestVerifyRootAndEmptyCommits(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		dir, git := gitRepo(t)
		write(t, dir, "root.txt", []byte("root\n"))
		hash := commit(t, git, "root")
		_, fragments, err := Chunks(dir, hash)
		if err != nil {
			t.Fatal(err)
		}
		requireVerify(t, dir, hash, tourFor(fragments))
	})

	t.Run("empty", func(t *testing.T) {
		dir, git := gitRepo(t)
		write(t, dir, "f.txt", []byte("same\n"))
		commit(t, git, "base")
		hash := commit(t, git, "empty")
		_, fragments, err := Chunks(dir, hash)
		if err != nil {
			t.Fatal(err)
		}
		if len(fragments) != 0 {
			t.Fatalf("fragments = %d, want 0", len(fragments))
		}
		requireVerify(t, dir, hash, &Tour{Version: 1, Title: "Empty"})
	})
}

func TestVerifyRenameBinaryAndMode(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "old.txt", []byte("rename me\n"))
	write(t, dir, "data.bin", []byte{0, 1, 2, 3, 4, 5})
	write(t, dir, "script.sh", []byte("#!/bin/sh\necho hi\n"))
	commit(t, git, "base")
	git("mv", "old.txt", "new.txt")
	write(t, dir, "data.bin", []byte{0, 9, 8, 7, 6, 5, 4, 3})
	if err := os.Chmod(filepath.Join(dir, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash := commit(t, git, "special changes")
	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(fragments, "\n")
	for _, marker := range []string{"rename from old.txt", "GIT binary patch", "old mode 100644", "new mode 100755"} {
		if !strings.Contains(all, marker) {
			t.Errorf("generated diff lacks %q:\n%s", marker, all)
		}
	}
	requireVerify(t, dir, hash, tourFor(fragments))
}

func TestChunksMultiHunkRenameStaysWhole(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte(numbered(1, 40)))
	commit(t, git, "base")
	git("mv", "f.txt", "moved.txt")
	content := strings.Replace(numbered(1, 40), "line 2\n", "line two\n", 1)
	content = strings.Replace(content, "line 38\n", "line thirty-eight\n", 1)
	write(t, dir, "moved.txt", []byte(content))
	hash := commit(t, git, "rename with two hunks")

	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 {
		t.Fatalf("want 1 whole-file fragment for a multi-hunk rename, got %d:\n%s", len(fragments), strings.Join(fragments, "\n===\n"))
	}
	if strings.Count(fragments[0], "\n@@")+1 < 2 {
		t.Fatalf("expected multiple hunks in rename fragment:\n%s", fragments[0])
	}
	requireVerify(t, dir, hash, tourFor(fragments))
}

func TestVerifyFragmentWithoutTrailingNewline(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte("a\nb\n"))
	commit(t, git, "base")
	write(t, dir, "f.txt", []byte("a\nc\n"))
	hash := commit(t, git, "edit")
	_, fragments, err := Chunks(dir, hash)
	if err != nil || len(fragments) != 1 {
		t.Fatalf("fragments = %v, err = %v", fragments, err)
	}
	tour := tourFor([]string{strings.TrimSuffix(fragments[0], "\n")})
	requireVerify(t, dir, hash, tour)
}

func TestVerifyMergeUsesFirstParent(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "base.txt", []byte("base\n"))
	commit(t, git, "base")
	git("checkout", "-q", "-b", "side")
	write(t, dir, "side.txt", []byte("side\n"))
	commit(t, git, "side")
	git("checkout", "-q", "main")
	write(t, dir, "main.txt", []byte("main\n"))
	commit(t, git, "main")
	git("merge", "-q", "--no-ff", "side", "-m", "merge", "-m", "Prompt: committour merge fixture")
	hash := strings.TrimSpace(git("rev-parse", "HEAD"))

	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(fragments, "\n")
	if !strings.Contains(all, "side.txt") || strings.Contains(all, "main.txt") {
		t.Fatalf("first-parent diff is wrong:\n%s", all)
	}
	requireVerify(t, dir, hash, tourFor(fragments))
}

func TestVerifyStaleAfterAmend(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte("base\n"))
	commit(t, git, "base")
	write(t, dir, "f.txt", []byte("old result\n"))
	oldHash := commit(t, git, "change")
	_, fragments, err := Chunks(dir, oldHash)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "f.txt", []byte("amended result\n"))
	git("add", "f.txt")
	git("commit", "-q", "--amend", "-m", "amended", "-m", "Prompt: committour amend fixture")
	newHash := strings.TrimSpace(git("rev-parse", "HEAD"))
	if oldHash == newHash {
		t.Fatal("amend did not change hash")
	}
	if _, err := Verify(dir, newHash, tourFor(fragments)); err == nil {
		t.Fatal("stale tour verified")
	}
}

func TestChunksAndVerifyIgnoreHostileConfig(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, ".gitattributes", []byte("*.txt diff=hostile\n"))
	write(t, dir, "f.txt", []byte(numbered(1, 30)))
	commit(t, git, "base")
	content := strings.Replace(numbered(1, 30), "line 3\n", "line 3 changed\n", 1)
	content = strings.Replace(content, "line 25\n", "line 25 changed\n", 1)
	write(t, dir, "f.txt", []byte(content))
	hash := commit(t, git, "change")
	_, want, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}

	textconv := filepath.Join(t.TempDir(), "textconv.sh")
	if err := os.WriteFile(textconv, []byte("#!/bin/sh\ntr 'a-z' 'A-Z' < \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("config", "diff.noprefix", "true")
	git("config", "diff.mnemonicPrefix", "true")
	git("config", "diff.srcPrefix", "wrong-old/")
	git("config", "diff.dstPrefix", "wrong-new/")
	git("config", "diff.hostile.textconv", textconv)
	git("config", "diff.external", "/does/not/exist")
	git("config", "color.ui", "always")
	git("config", "log.showSignature", "true")
	git("config", "log.showRoot", "false")
	git("config", "diff.submodule", "log")
	git("config", "diff.ignoreSubmodules", "all")

	_, got, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Fatalf("hostile config changed generated fragments:\ngot:\n%s\nwant:\n%s", strings.Join(got, ""), strings.Join(want, ""))
	}
	for _, fragment := range got {
		if !strings.HasPrefix(fragment, "diff --git a/") || !strings.Contains(fragment, "\n--- a/") || !strings.Contains(fragment, "\n+++ b/") {
			t.Fatalf("prefix config leaked into fragment:\n%s", fragment)
		}
	}
	// Apply intentionally receives no diff-prefix config overrides. Repository
	// diff.noprefix and src/dst prefix settings do not affect patch parsing.
	requireVerify(t, dir, hash, tourFor(got))
}

func TestVerifyValidationAndWarnings(t *testing.T) {
	for name, tour := range map[string]*Tour{
		"nil":          nil,
		"version":      {Version: 2},
		"neither":      {Version: 1, Chunks: []TourChunk{{Comment: "x"}}},
		"both":         {Version: 1, Chunks: []TourChunk{{Header: "h", Patch: "p"}}},
		"blank header": {Version: 1, Chunks: []TourChunk{{Header: " "}}},
		"blank patch":  {Version: 1, Chunks: []TourChunk{{Patch: " "}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(".", "HEAD", tour); err == nil {
				t.Fatal("Verify succeeded")
			}
		})
	}

	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte("a\n"))
	hash := commit(t, git, "root")
	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	tour := tourFor(fragments)
	tour.Chunks[0].Comment = ""
	warnings, err := Verify(dir, hash, tour)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no comment") {
		t.Fatalf("warnings = %v", warnings)
	}
	tour.Chunks[0].Trivial = true
	requireVerify(t, dir, hash, tour)
}

func TestParseTourJSON(t *testing.T) {
	data := []byte(`{"version":1,"title":"T","chunks":[{"header":"## Sec"},{"patch":"diff","comment":"c"},{"patch":"trivial","trivial":true}]}`)
	tour, err := ParseTour(data)
	if err != nil {
		t.Fatal(err)
	}
	if tour.Version != 1 || len(tour.Chunks) != 3 || tour.Chunks[0].Header != "## Sec" || tour.Chunks[1].Patch != "diff" || !tour.Chunks[2].Trivial {
		t.Fatalf("tour = %+v", tour)
	}
	if _, err := ParseTour([]byte(`{"version":1,`)); err == nil {
		t.Fatal("malformed JSON parsed")
	}
	if _, err := ParseTour([]byte(`null`)); err == nil {
		t.Fatal("null parsed")
	}
}

func TestNotesRoundTrip(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "a.txt", []byte("a\n"))
	first := commit(t, git, "first")
	write(t, dir, "a.txt", []byte("b\n"))
	second := commit(t, git, "second")

	if _, err := ReadNote(dir, first); !errors.Is(err, ErrNoNote) {
		t.Fatalf("ReadNote error = %v", err)
	}
	notes, err := ListNotes(dir)
	if err != nil || len(notes) != 0 {
		t.Fatalf("ListNotes = %v, %v", notes, err)
	}
	payload := []byte(`{"version":1,"chunks":[]}`)
	if err := WriteNote(dir, first, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadNote(dir, first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != string(payload) {
		t.Fatalf("note = %q", got)
	}
	notes, err = ListNotes(dir)
	if err != nil || len(notes) != 1 || !notes[first] || notes[second] {
		t.Fatalf("ListNotes = %v, %v", notes, err)
	}
}

func TestTourChunkJSON(t *testing.T) {
	data, err := json.Marshal(TourChunk{Patch: "p", Comment: "c", Trivial: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"patch":"p","comment":"c","trivial":true}` {
		t.Fatalf("json = %s", data)
	}
}

func TestResolveChunkReferences(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "a.txt", []byte(numbered(1, 30)))
	write(t, dir, "b.txt", []byte("b\n"))
	commit(t, git, "base")
	write(t, dir, "a.txt", []byte("CHANGED\n"+numbered(2, 29)+"ALSO CHANGED\n"))
	write(t, dir, "b.txt", []byte("B\n"))
	hash := commit(t, git, "change")

	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 3 {
		t.Fatalf("got %d fragments, want 3", len(fragments))
	}

	ref := func(i int) *int { return &i }
	tour := &Tour{Version: 1, Chunks: []TourChunk{
		{Header: "## H"},
		{Ref: ref(1), Comment: "second hunk first"},
		{Ref: ref(0), Comment: "first hunk"},
		{Ref: ref(2), Trivial: true},
	}}
	requireVerify(t, dir, hash, tour)
	if tour.Chunks[1].Patch != fragments[1] || tour.Chunks[1].Ref != nil {
		t.Fatalf("chunk not resolved: %+v", tour.Chunks[1])
	}

	for name, chunks := range map[string][]TourChunk{
		"out of range":   {{Ref: ref(9)}},
		"negative":       {{Ref: ref(-1)}},
		"ref and patch":  {{Ref: ref(0), Patch: "diff"}},
		"ref and header": {{Ref: ref(0), Header: "## H"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(dir, hash, &Tour{Version: 1, Chunks: chunks}); err == nil {
				t.Fatal("Verify succeeded")
			}
		})
	}

	// Duplicate references must fail tree verification (chunk applied twice).
	dup := &Tour{Version: 1, Chunks: []TourChunk{
		{Ref: ref(0), Trivial: true}, {Ref: ref(0), Trivial: true},
	}}
	if _, err := Verify(dir, hash, dup); err == nil {
		t.Fatal("duplicate reference verified")
	}
}

func TestMeta(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "dir/file.go", []byte(numbered(1, 20)))
	commit(t, git, "base")
	write(t, dir, "dir/file.go", []byte("x\n"+numbered(2, 19)+"y\ny2\n"))
	hash := commit(t, git, "change")

	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 2 {
		t.Fatalf("got %d fragments", len(fragments))
	}
	m := Meta(fragments[0])
	if m.File != "dir/file.go" || !strings.HasPrefix(m.Hunk, "@@ ") || m.Adds != 1 || m.Dels != 1 {
		t.Fatalf("meta = %+v", m)
	}
	m = Meta(fragments[1])
	if m.File != "dir/file.go" || m.Adds != 2 || m.Dels != 1 {
		t.Fatalf("meta = %+v", m)
	}

	// Deletion reports the old path; rename fragments report the new path.
	git("rm", "-q", "dir/file.go")
	delHash := commit(t, git, "delete")
	_, delFrags, err := Chunks(dir, delHash)
	if err != nil {
		t.Fatal(err)
	}
	if m := Meta(delFrags[0]); m.File != "dir/file.go" {
		t.Fatalf("deletion meta = %+v", m)
	}

	write(t, dir, "ren.txt", []byte(numbered(1, 50)))
	commit(t, git, "add ren")
	git("mv", "ren.txt", "ren2.txt")
	renHash := commit(t, git, "rename")
	_, renFrags, err := Chunks(dir, renHash)
	if err != nil {
		t.Fatal(err)
	}
	if m := Meta(renFrags[0]); m.File != "ren2.txt" || m.Hunk != "" {
		t.Fatalf("rename meta = %+v", m)
	}

	// Diff-like content inside hunk bodies must not clobber file or counts.
	write(t, dir, "notes.md", []byte("one\n"))
	commit(t, git, "add notes")
	write(t, dir, "notes.md", []byte("one\n++ b/evil\n-- a/evil\n"))
	trapHash := commit(t, git, "diff-like")
	_, trapFrags, err := Chunks(dir, trapHash)
	if err != nil {
		t.Fatal(err)
	}
	if m := Meta(trapFrags[0]); m.File != "notes.md" || m.Adds != 2 || m.Dels != 0 {
		t.Fatalf("diff-like meta = %+v", m)
	}
}

func TestSubject(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "a.txt", []byte("a\n"))
	hash := commit(t, git, "my subject line")
	subject, err := Subject(dir, hash)
	if err != nil || subject != "my subject line" {
		t.Fatalf("Subject = %q, %v", subject, err)
	}
}

func TestGitEnvIgnoresRepoOverrides(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte("a\n"))
	hash := commit(t, git, "base")

	other, otherGit := gitRepo(t)
	write(t, other, "g.txt", []byte("b\n"))
	commit(t, otherGit, "other")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	gotHash, fragments, err := Chunks(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != hash {
		t.Fatalf("GIT_DIR override leaked: got %s, want %s", gotHash, hash)
	}
	requireVerify(t, dir, hash, tourFor(fragments))
}

func TestVerifyShallowCloneParent(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "f.txt", []byte(numbered(1, 5)))
	commit(t, git, "base")
	write(t, dir, "f.txt", []byte(strings.Replace(numbered(1, 5), "line 3\n", "line three\n", 1)))
	hash := commit(t, git, "edit")
	_, fragments, err := Chunks(dir, hash)
	if err != nil {
		t.Fatal(err)
	}

	shallow := filepath.Join(t.TempDir(), "shallow")
	git("clone", "--depth=1", "file://"+dir, shallow)
	// The parent commit object is absent in a depth-1 clone. Verify must
	// fail with an error, not silently compare against the empty tree.
	if _, err := Verify(shallow, hash, tourFor(fragments)); err == nil {
		t.Fatal("expected error verifying at a shallow boundary")
	}
}
