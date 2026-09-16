// Package committour parses and stores guided tours of Git commits.
package committour

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const notesRef = "shelley-tour"

type Tour struct {
	Version int         `json:"version"`
	Title   string      `json:"title,omitempty"`
	Intro   string      `json:"intro,omitempty"`
	Chunks  []TourChunk `json:"chunks"`
}

// TourChunk is either a markdown section header, a self-contained patch, or
// a reference to a suggested chunk id (resolved to a patch by Resolve).
type TourChunk struct {
	Header  string `json:"header,omitempty"`
	Patch   string `json:"patch,omitempty"`
	Ref     *int   `json:"ref,omitempty"`
	Comment string `json:"comment,omitempty"`
	Trivial bool   `json:"trivial,omitempty"`
}

// FragmentMeta describes a patch fragment for compact listings.
type FragmentMeta struct {
	File string // new path (or old path for deletions)
	Hunk string // "@@ ... @@" header, empty for hunkless fragments
	Adds int
	Dels int
}

// Meta extracts listing metadata from a patch fragment. File headers are only
// honored before the first hunk: added lines whose content begins with "++ "
// or "-- " would otherwise masquerade as +++/--- headers.
func Meta(fragment string) FragmentMeta {
	var m FragmentMeta
	var oldFile string
	for _, line := range splitLines(fragment) {
		switch {
		case strings.HasPrefix(line, "@@"):
			if m.Hunk == "" {
				m.Hunk = strings.TrimSuffix(line, "\n")
			}
		case m.Hunk != "":
			if strings.HasPrefix(line, "+") {
				m.Adds++
			} else if strings.HasPrefix(line, "-") {
				m.Dels++
			}
		case strings.HasPrefix(line, "+++ "):
			m.File = cutPathPrefix(strings.TrimSuffix(line[4:], "\n"), "b/")
		case strings.HasPrefix(line, "--- "):
			oldFile = cutPathPrefix(strings.TrimSuffix(line[4:], "\n"), "a/")
		case strings.HasPrefix(line, "rename to "), strings.HasPrefix(line, "copy to "):
			if m.File == "" {
				_, m.File, _ = strings.Cut(strings.TrimSuffix(line, "\n"), " to ")
			}
		}
	}
	if m.File == "" || m.File == "/dev/null" {
		m.File = oldFile
	}
	if m.File == "" {
		// Fall back to the "diff --git a/x b/y" line (e.g. binary changes).
		for _, line := range splitLines(fragment) {
			if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
				if _, after, found := strings.Cut(strings.TrimSuffix(rest, "\n"), " b/"); found {
					m.File = after
				}
				break
			}
		}
	}
	return m
}

func cutPathPrefix(path, prefix string) string {
	if rest, ok := strings.CutPrefix(path, prefix); ok {
		return rest
	}
	return path
}

// Subject returns the commit's subject line.
func Subject(dir, commit string) (string, error) {
	out, err := gitOutput(dir, "", nil, "log", "-1", "--format=%s", commit+"^{commit}", "--")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Resolve replaces ref entries with the referenced suggested patches for the
// given commit. It reports whether any refs were resolved. Tours without refs
// are returned unchanged.
func Resolve(dir, commit string, tour *Tour) (bool, error) {
	if tour == nil {
		return false, errors.New("tour is nil")
	}
	var refs []int
	for i, entry := range tour.Chunks {
		if entry.Ref == nil {
			continue
		}
		if entry.Header != "" || entry.Patch != "" {
			return false, fmt.Errorf("chunks[%d] has ref alongside header or patch", i)
		}
		refs = append(refs, i)
	}
	if len(refs) == 0 {
		return false, nil
	}
	_, fragments, err := Chunks(dir, commit)
	if err != nil {
		return false, err
	}
	for _, i := range refs {
		id := *tour.Chunks[i].Ref
		if len(fragments) == 0 {
			return false, fmt.Errorf("chunks[%d] references chunk %d, but the commit has no chunks", i, id)
		}
		if id < 0 || id >= len(fragments) {
			return false, fmt.Errorf("chunks[%d] references chunk %d, but the commit has chunks 0..%d", i, id, len(fragments)-1)
		}
		tour.Chunks[i].Patch = fragments[id]
		tour.Chunks[i].Ref = nil
	}
	return true, nil
}

// Chunks returns the full commit hash and one suggested patch fragment per hunk.
func Chunks(dir, commit string) (string, []string, error) {
	hashBytes, err := gitOutput(dir, "", nil, "rev-parse", commit+"^{commit}")
	if err != nil {
		return "", nil, err
	}
	hash := strings.TrimSpace(string(hashBytes))
	if hash == "" || strings.ContainsAny(hash, "\r\n") {
		return "", nil, fmt.Errorf("git rev-parse returned invalid hash %q", hash)
	}

	diff, err := gitOutput(
		dir, "", nil,
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"-c", "diff.srcPrefix=a/",
		"-c", "diff.dstPrefix=b/",
		"-c", "diff.submodule=short",
		"-c", "diff.ignoreSubmodules=none",
		"-c", "log.showRoot=true",
		"show", hash,
		"--format=", "--no-color", "--no-show-signature", "--binary",
		"--no-ext-diff", "--no-textconv", "--find-renames",
		"--diff-merges=first-parent", "-O/dev/null",
	)
	if err != nil {
		return "", nil, err
	}
	fragments, err := splitDiff(string(diff))
	if err != nil {
		return "", nil, err
	}
	return hash, fragments, nil
}

func splitDiff(diff string) ([]string, error) {
	lines := splitLines(diff)
	files := make([][]string, 0)
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, nil)
		}
		if len(files) == 0 {
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("unexpected diff content before file header: %q", strings.TrimSpace(line))
			}
			continue
		}
		files[len(files)-1] = append(files[len(files)-1], line)
	}

	fragments := make([]string, 0)
	for _, file := range files {
		var hunks []int
		renamed := false
		for i, line := range file {
			if strings.HasPrefix(line, "@@") {
				hunks = append(hunks, i)
			}
			if len(hunks) == 0 && (strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "copy from ")) {
				renamed = true
			}
		}
		// Renames and copies stay whole: later hunks target a path that
		// does not exist until the fragment carrying the rename applies,
		// and repeating the rename metadata per hunk breaks git apply.
		if len(hunks) == 0 || renamed {
			fragments = append(fragments, strings.Join(file, ""))
			continue
		}
		header := strings.Join(file[:hunks[0]], "")
		for i, start := range hunks {
			end := len(file)
			if i+1 < len(hunks) {
				end = hunks[i+1]
			}
			fragments = append(fragments, header+strings.Join(file[start:end], ""))
		}
	}
	return fragments, nil
}

func ParseTour(data []byte) (*Tour, error) {
	var tour *Tour
	if err := json.Unmarshal(data, &tour); err != nil {
		return nil, fmt.Errorf("parse tour: %w", err)
	}
	if tour == nil {
		return nil, errors.New("parse tour: expected JSON object")
	}
	return tour, nil
}

// Verify applies the tour patches to a temporary index containing the commit's
// first-parent tree and requires the resulting tree to equal the commit tree.
// Chunk-index references are resolved against the commit's suggested chunks.
func Verify(dir, commit string, tour *Tour) ([]string, error) {
	if _, err := Resolve(dir, commit, tour); err != nil {
		return nil, err
	}
	warnings, patches, err := validateTour(tour)
	if err != nil {
		return warnings, err
	}

	tmp, err := os.MkdirTemp("", "shelley-commit-tour-*")
	if err != nil {
		return warnings, fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")

	hashBytes, err := gitOutput(dir, index, nil, "rev-parse", commit+"^{commit}")
	if err != nil {
		return warnings, err
	}
	hash := strings.TrimSpace(string(hashBytes))
	wantBytes, err := gitOutput(dir, index, nil, "rev-parse", hash+"^{tree}")
	if err != nil {
		return warnings, err
	}
	want := strings.TrimSpace(string(wantBytes))

	// Determine the base tree: the first parent, or the empty tree for a
	// parentless commit. Read parents from the raw commit object; traversal
	// commands hide parents at shallow-clone boundaries.
	rawBytes, err := gitOutput(dir, index, nil, "cat-file", "commit", hash)
	if err != nil {
		return warnings, err
	}
	var parentHashes []string
	for _, line := range strings.Split(string(rawBytes), "\n") {
		if line == "" {
			break // end of commit header
		}
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			parentHashes = append(parentHashes, p)
		}
	}
	var parent string
	if len(parentHashes) == 0 {
		emptyBytes, err := gitOutput(dir, index, nil, "hash-object", "-t", "tree", os.DevNull)
		if err != nil {
			return warnings, err
		}
		parent = strings.TrimSpace(string(emptyBytes))
	} else {
		parentBytes, err := gitOutput(dir, index, nil, "rev-parse", parentHashes[0]+"^{tree}")
		if err != nil {
			return warnings, err
		}
		parent = strings.TrimSpace(string(parentBytes))
	}

	if _, err := gitOutput(dir, index, nil, "read-tree", parent); err != nil {
		return warnings, err
	}
	if len(patches) > 0 {
		var patch strings.Builder
		for _, p := range patches {
			patch.WriteString(p)
			if !strings.HasSuffix(p, "\n") {
				patch.WriteString("\n")
			}
		}
		if _, err := gitOutput(dir, index, strings.NewReader(patch.String()),
			"apply", "--cached", "--unidiff-zero", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return warnings, err
		}
	}
	gotBytes, err := gitOutput(dir, index, nil, "write-tree")
	if err != nil {
		return warnings, err
	}
	got := strings.TrimSpace(string(gotBytes))
	if got != want {
		return warnings, fmt.Errorf("tour produces tree %s, want %s", got, want)
	}
	return warnings, nil
}

func validateTour(tour *Tour) ([]string, []string, error) {
	if tour == nil {
		return nil, nil, errors.New("tour is nil")
	}
	if tour.Version != 1 {
		return nil, nil, fmt.Errorf("version is %d, want 1", tour.Version)
	}
	var warnings, patches []string
	for i, entry := range tour.Chunks {
		headerSet := entry.Header != ""
		patchSet := entry.Patch != ""
		if headerSet == patchSet {
			return warnings, nil, fmt.Errorf("chunks[%d] must have exactly one of header and patch", i)
		}
		if headerSet {
			if strings.TrimSpace(entry.Header) == "" {
				return warnings, nil, fmt.Errorf("chunks[%d] has an empty header", i)
			}
			continue
		}
		if strings.TrimSpace(entry.Patch) == "" {
			return warnings, nil, fmt.Errorf("chunks[%d] has an empty patch", i)
		}
		patches = append(patches, entry.Patch)
		if !entry.Trivial && strings.TrimSpace(entry.Comment) == "" {
			meta := Meta(entry.Patch)
			warnings = append(warnings, fmt.Sprintf("chunks[%d] (%s %s) is non-trivial but has no comment", i, meta.File, meta.Hunk))
		}
	}
	return warnings, patches, nil
}

// ErrNoNote reports that a commit has no tour note attached.
var ErrNoNote = errors.New("no tour note")

func ReadNote(dir, commit string) ([]byte, error) {
	data, err := gitOutput(dir, "", nil, "notes", "--ref="+notesRef, "show", commit)
	if err != nil {
		if strings.Contains(err.Error(), "no note found") {
			return nil, fmt.Errorf("%w for %s", ErrNoNote, commit)
		}
		return nil, err
	}
	return data, nil
}

func WriteNote(dir, commit string, data []byte) error {
	file, err := os.CreateTemp("", "shelley-tour-*.json")
	if err != nil {
		return fmt.Errorf("create tour note file: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write tour note file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close tour note file: %w", err)
	}
	// Retry on notes-ref lock contention: concurrent attachers (e.g. parallel
	// subagents annotating different commits) race on refs/notes/<ref>.lock.
	for attempt := 0; ; attempt++ {
		_, err = gitOutput(dir, "", nil, "notes", "--ref="+notesRef, "add", "-f", "-F", name, commit)
		if err == nil || attempt >= 5 || !strings.Contains(err.Error(), ".lock") {
			return err
		}
		time.Sleep(time.Duration(50<<attempt) * time.Millisecond)
	}
}

func ListNotes(dir string) (map[string]bool, error) {
	output, err := gitOutput(dir, "", nil, "notes", "--ref="+notesRef, "list")
	if err != nil {
		return nil, err
	}
	notes := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid git notes list line %q", line)
		}
		notes[fields[1]] = true
	}
	return notes, nil
}

// gitOutput runs git -C dir with a sanitized environment: repo-locating
// variables are stripped so dir always wins, and index (if non-empty) is used
// as GIT_INDEX_FILE so plumbing never touches the real index.
func gitOutput(dir, index string, stdin *strings.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv(index)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return output, nil
}

func gitEnv(index string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "GIT_INDEX_FILE", "GIT_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		env = append(env, value)
	}
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	return env
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
