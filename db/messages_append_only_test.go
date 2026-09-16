package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMessagesAreAppendOnly fails if a new UPDATE against the messages table
// appears in the query file.
//
// Message rows are an append-only log, and a lot of the system quietly depends
// on that: the browser caches messages keyed by (conversation_id, sequence_id)
// and refreshes by fetching only the tail (so it never re-reads a row it
// already has), forks copy rows wholesale, and the stream contract is that a
// sequence_id is delivered exactly once. An UPDATE is invisible to all of
// those — it produces stale caches with no error anywhere.
//
// Mutable per-conversation state belongs on the conversations row, which is
// designed for it and is re-broadcast on change. If you need to change
// something about a message, append a new message instead.
//
// The one permitted exception is UpdateMessageUserData, which exists solely to
// exercise the messages_fts AFTER UPDATE trigger from a test.
func TestMessagesAreAppendOnly(t *testing.T) {
	// UpdateMessageUserData exists solely to exercise the messages_fts AFTER
	// UPDATE trigger from a test; nothing in production calls it.
	allowed := map[string]bool{"UpdateMessageUserData": true}

	// Any statement that can change an existing messages row, not just
	// "UPDATE messages": REPLACE and INSERT ... ON CONFLICT DO UPDATE do it
	// too, and whitespace/newlines between the keyword and the table name are
	// legal SQL.
	mutators := []*regexp.Regexp{
		regexp.MustCompile(`(?is)\bUPDATE\s+(?:OR\s+\w+\s+)?["'` + "`" + `\[]?messages["'` + "`" + `\]]?\b`),
		regexp.MustCompile(`(?is)\bREPLACE\s+INTO\s+["'` + "`" + `\[]?messages["'` + "`" + `\]]?\b`),
		regexp.MustCompile(`(?is)\bINSERT\s+OR\s+REPLACE\s+INTO\s+["'` + "`" + `\[]?messages["'` + "`" + `\]]?\b`),
		regexp.MustCompile(`(?is)\bON\s+CONFLICT\b[^;]*?\bDO\s+UPDATE\b`),
	}

	// Scan every query file, not just messages.sql: nothing stops a new
	// mutation landing in conversations.sql.
	queryFiles, err := filepath.Glob("query/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(queryFiles) == 0 {
		t.Fatal("no query files found; did this test move?")
	}
	nameRe := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	for _, file := range queryFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		locs := nameRe.FindAllStringSubmatchIndex(text, -1)
		for i, loc := range locs {
			name := text[loc[2]:loc[3]]
			end := len(text)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			stmt := stripSQLComments(text[loc[1]:end])
			// ON CONFLICT DO UPDATE only matters if this statement targets
			// messages at all.
			targetsMessages := regexp.MustCompile(`(?is)\bINTO\s+["'` + "`" + `\[]?messages["'` + "`" + `\]]?\b`).MatchString(stmt)
			for mi, re := range mutators {
				if mi == 3 && !targetsMessages {
					continue
				}
				if !re.MatchString(stmt) {
					continue
				}
				if allowed[name] {
					continue
				}
				t.Errorf("%s: query %q can modify an existing messages row, but message rows are append-only.\n"+
					"Browser caches key on (conversation_id, sequence_id) and only ever fetch the tail, so an\n"+
					"in-place change is never picked up; forks copy rows; a sequence_id is streamed exactly once.\n"+
					"Record data that arrives later by APPENDING a row (see CreateSlugMessage), never by rewriting one.",
					file, name)
			}
		}
	}

	// Raw SQL in Go bypasses the query files entirely.
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	rawRe := regexp.MustCompile(`(?is)\bUPDATE\s+messages\b`)
	for _, file := range goFiles {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if rawRe.MatchString(string(src)) {
			t.Errorf("%s contains a raw UPDATE against messages; message rows are append-only", file)
		}
	}

	// A migration can install an UPDATE trigger on messages, which mutates
	// rows without any query naming it.
	schemaFiles, err := filepath.Glob("schema/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The FTS index is maintained by triggers that update the fts shadow
	// table, not the messages table; those are fine.
	trigRe := regexp.MustCompile(`(?is)CREATE\s+TRIGGER\s+\w+[^;]*?\bUPDATE\s+messages\b`)
	for _, file := range schemaFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if trigRe.MatchString(string(src)) {
			t.Errorf("%s installs a trigger that updates messages; message rows are append-only", file)
		}
	}
}

// stripSQLComments removes -- line comments so a comment mentioning "UPDATE
// messages" (this file's own rationale gets quoted a lot) isn't a false hit.
func stripSQLComments(s string) string {
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}
