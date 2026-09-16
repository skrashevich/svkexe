package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"shelley.exe.dev/committour"
)

func runTour(args []string) {
	if len(args) == 0 {
		tourUsage()
		os.Exit(1)
	}

	fs := flag.NewFlagSet("tour "+args[0], flag.ExitOnError)
	dir := fs.String("C", ".", "Git repository directory")
	indexOnly := fs.Bool("index", false, "chunks: print a compact per-chunk index instead of JSON patch bodies")
	only := fs.String("only", "", "chunks: comma-separated chunk ids/ranges (e.g. 0,3-5) or a file path")
	text := fs.Bool("text", false, "chunks: print raw patch text with '=== chunk N file' separators instead of JSON")
	fs.Parse(args[1:])
	rest := fs.Args()

	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch args[0] {
	case "chunks":
		if *indexOnly && *text {
			fail(errors.New("-index and -text are mutually exclusive"))
		}
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: shelley tour chunks [-C dir] [-index|-text] [-only ids|file] <commit>")
			os.Exit(1)
		}
		hash, fragments, err := committour.Chunks(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		subject, err := committour.Subject(*dir, hash)
		if err != nil {
			fail(err)
		}
		ids := make([]int, len(fragments))
		for i := range ids {
			ids[i] = i
		}
		if *only != "" {
			if ids, err = selectChunks(*only, fragments); err != nil {
				fail(err)
			}
		}
		if *indexOnly {
			fmt.Printf("commit %s %s\n", hash, subject)
			lastFile := ""
			for _, id := range ids {
				meta := committour.Meta(fragments[id])
				if meta.File != lastFile {
					fmt.Printf("%s\n", meta.File)
					lastFile = meta.File
				}
				hunk := meta.Hunk
				if hunk == "" {
					hunk = "(no hunk: mode/rename/binary)"
				}
				fmt.Printf("  %3d  +%d -%d  %s\n", id, meta.Adds, meta.Dels, hunk)
			}
			return
		}
		if *text {
			for _, id := range ids {
				fmt.Printf("=== chunk %d %s\n%s", id, committour.Meta(fragments[id]).File, fragments[id])
			}
			return
		}
		type chunkOut struct {
			ID    int    `json:"id"`
			File  string `json:"file"`
			Patch string `json:"patch"`
		}
		chunks := make([]chunkOut, len(ids))
		for i, id := range ids {
			chunks[i] = chunkOut{id, committour.Meta(fragments[id]).File, fragments[id]}
		}
		out, err := json.MarshalIndent(struct {
			Hash    string     `json:"hash"`
			Subject string     `json:"subject"`
			Chunks  []chunkOut `json:"chunks"`
		}{hash, subject, chunks}, "", "  ")
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s\n", out)

	case "scaffold":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: shelley tour scaffold [-C dir] <commit>")
			os.Exit(1)
		}
		hash, fragments, err := committour.Chunks(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		subject, err := committour.Subject(*dir, hash)
		if err != nil {
			fail(err)
		}
		tour := committour.Tour{Version: 1, Title: subject, Chunks: []committour.TourChunk{}}
		for id := range fragments {
			entry := committour.TourChunk{Ref: &id}
			if generatedPath(committour.Meta(fragments[id]).File) {
				entry.Trivial = true
			}
			tour.Chunks = append(tour.Chunks, entry)
		}
		out, err := json.MarshalIndent(tour, "", "  ")
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s\n", out)

	case "verify", "attach":
		if len(rest) != 2 {
			fmt.Fprintf(os.Stderr, "Usage: shelley tour %s [-C dir] <commit> <tour.json>\n", args[0])
			os.Exit(1)
		}
		data, err := os.ReadFile(rest[1])
		if err != nil {
			fail(err)
		}
		tour, err := committour.ParseTour(data)
		if err != nil {
			fail(err)
		}
		resolved, err := committour.Resolve(*dir, rest[0], tour)
		if err != nil {
			fail(err)
		}
		warnings, err := committour.Verify(*dir, rest[0], tour)
		for _, warning := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
		if err != nil {
			fail(err)
		}
		if args[0] == "verify" {
			fmt.Printf("tour for %s verifies\n", rest[0])
			return
		}
		if resolved {
			// Store the resolved tour so readers need not re-derive chunks.
			data, err = json.MarshalIndent(tour, "", "  ")
			if err != nil {
				fail(err)
			}
		}
		if err := committour.WriteNote(*dir, rest[0], data); err != nil {
			fail(err)
		}
		fmt.Printf("attached tour note to %s\n", rest[0])

	case "show":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: shelley tour show [-C dir] <commit>")
			os.Exit(1)
		}
		data, err := committour.ReadNote(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		if _, err := os.Stdout.Write(data); err != nil {
			fail(err)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown tour subcommand: %s\n", args[0])
		tourUsage()
		os.Exit(1)
	}
}

func tourUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  shelley tour chunks [-C dir] [-index|-text] [-only ids|file] <commit>")
	fmt.Fprintln(os.Stderr, "                                                     Print suggested patch fragments")
	fmt.Fprintln(os.Stderr, "  shelley tour scaffold [-C dir] <commit>            Print a complete all-refs tour skeleton")
	fmt.Fprintln(os.Stderr, "  shelley tour verify [-C dir] <commit> <tour.json>  Verify a tour against the commit tree")
	fmt.Fprintln(os.Stderr, "  shelley tour attach [-C dir] <commit> <tour.json>  Verify, then store the tour as a git note")
	fmt.Fprintln(os.Stderr, "  shelley tour show [-C dir] <commit>                Print the stored tour note")
}

// generatedPath reports whether a path looks machine-generated or otherwise
// uninteresting to narrate: lock files and common codegen output.
func generatedPath(path string) bool {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	switch base {
	case "go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "Cargo.lock", "poetry.lock", "uv.lock", "composer.lock", "Gemfile.lock":
		return true
	}
	for _, suffix := range []string{".pb.go", ".pb.gw.go", "_pb2.py", ".gen.go", "_gen.go", ".sql.go", ".min.js", ".min.css"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, dir := range strings.Split(path, "/") {
		if dir == "generated" || dir == "node_modules" || dir == "vendor" {
			return true
		}
	}
	return false
}

// selectChunks parses spec as comma-separated ids/ranges ("0,3-5") or, if
// that fails and the spec has no comma, as a file path matched against each
// fragment's file.
func selectChunks(spec string, fragments []string) ([]int, error) {
	var ids []int
	numeric := true
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		start, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			numeric = false
			break
		}
		end := start
		if isRange {
			if end, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
				numeric = false
				break
			}
			if end < start {
				return nil, fmt.Errorf("invalid chunk range %q: start exceeds end", strings.TrimSpace(part))
			}
		}
		for i := start; i <= end; i++ {
			ids = append(ids, i)
		}
	}
	if numeric {
		if len(fragments) == 0 {
			return nil, fmt.Errorf("commit has no chunks")
		}
		for _, id := range ids {
			if id < 0 || id >= len(fragments) {
				return nil, fmt.Errorf("chunk %d out of range 0..%d", id, len(fragments)-1)
			}
		}
		return ids, nil
	}
	if strings.Contains(spec, ",") {
		return nil, fmt.Errorf("invalid chunk selection %q: want ids/ranges like 0,3-5 or a file path", spec)
	}
	// File-path selection.
	ids = nil
	for i, fragment := range fragments {
		if committour.Meta(fragment).File == spec {
			ids = append(ids, i)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no chunks match %q as a file path or id list", spec)
	}
	return ids, nil
}
