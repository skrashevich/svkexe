---
name: commit-tour
description: Use when the user or project guidance (AGENTS.md) asks for a "commit tour", annotated commits, or commit annotations. Produces a narrative walkthrough of a commit's diff, stored as a git note and rendered in Shelley's diff UI.
---

A commit guided tour is a JSON annotation of a commit's diff, stored in the git
note ref `shelley-tour`. Shelley's diff UI shows a badge on annotated commits
and renders the tour: important changes first with commentary, trivial changes
collapsed.

## Workflow

Annotate a commit right after making it. Delegate to a subagent when the commit
is large; pass it the commit hash, the repo directory, and this skill.

Work files-first, then chunks: survey the changed files, plan the narrative
order, and only then read patch bodies — selectively. Never print the full
chunks JSON into your context for anything beyond a small commit.

1. Survey the commit with the chunk index:

   ```
   shelley tour chunks [-C <repo-dir>] -index <commit>
   ```

   This prints the commit subject and one line per suggested chunk — id,
   +adds/-dels, and `@@` header — grouped by file, with no patch bodies.
   Classify files into tiers: data model / schema / API first, then core
   logic, tests, and finally generated files (sqlc, protobuf), lock files,
   and boilerplate. Whole tiers of generated or mechanical chunks can be
   marked trivial without ever reading their bodies.

2. Read only the chunks you need to write commentary for:

   ```
   shelley tour chunks [-C <repo-dir>] -text -only 4,7-9 <commit>
   shelley tour chunks [-C <repo-dir>] -text -only path/to/file.go <commit>
   ```

   `-only` takes chunk ids/ranges or a single file path. `-text` prints raw
   patch text with `=== chunk N <file>` separators; without it you get JSON:
   the full `hash`, the commit `subject`, and a `chunks` array of
   `{"id": <id>, "file": "...", "patch": "..."}` objects. Each suggestion is
   a self-contained, git-apply-able fragment: its file headers plus one hunk,
   or the header block alone for a hunkless binary, rename, or mode change.

3. Write the tour JSON to a temp file. Start from a scaffold and reference
   suggested chunks by id — never re-type patch text:

   ```
   shelley tour scaffold [-C <repo-dir>] <commit> > tour.json
   ```

   The scaffold lists every chunk as a `{"ref": <id>}` entry in diff order,
   with lock files and codegen output pre-marked trivial, so coverage is
   guaranteed by construction. Then reorder entries by importance, insert
   headers, and add comments:

   ```json
   {
     "version": 1,
     "title": "Short plain-text title",
     "intro": "Markdown intro: what this commit does and why.",
     "chunks": [
       {"header": "## The data model"},
       {"ref": 4, "comment": "Why this shape matters."},
       {"patch": "diff --git a/server/model.go b/server/model.go\n...", "comment": "A hand-split slice."},
       {"header": "## Supporting changes"},
       {"ref": 0, "trivial": true},
       {"ref": 1, "trivial": true}
     ]
   }
   ```

   Every entry has exactly one of `header`, `ref` (a suggested-chunk id), or
   `patch` (literal patch text, for hand-split hunks). Ref and patch entries
   may also have `comment` and `trivial`. `attach` stores refs resolved to
   their patch text, so the note remains self-contained.

   For big commits, edit the scaffold with a short script that maps ids to
   entries (e.g. `add(14, 'comment')`, `header('## ...')`) rather than
   rewriting it by hand.

4. Verify, then attach:

   ```
   shelley tour verify [-C <repo-dir>] <commit> tour.json
   shelley tour attach [-C <repo-dir>] <commit> tour.json
   ```

   `verify` applies all entries to the commit's parent tree and requires the
   exact commit tree. Gaps, overlaps, duplicate chunks, edited lines, or bad
   headers fail with Git's error text. `attach` verifies and writes the note
   (re-running overwrites; concurrent attaches retry ref locks automatically).
   `shelley tour show <commit>` prints an existing tour.

## Writing a good tour

- **Data model first.** Open with changed types, schemas, formats, or APIs so
  readers understand the shapes involved.
- **Important bits first.** After the data model, order entries by importance, not file
  order: core logic and decisions, tests, then generated files, lock files,
  imports, boilerplate, and renames.
- **Cover the whole diff.** Verification reconstructs the commit tree from the
  parent, so every change must appear exactly once. Check ids off against the
  `-index` listing before verifying.
- Split large suggested hunks into logical patch entries by copying the file
  header block and rewriting each slice's `@@` header. Old starts use parent
  coordinates; new starts use final-file coordinates. Zero context is fine.
  Keep each `\ No newline at end of file` marker with its hunk's last line.
- Entries may be reordered freely, including slices from the same original
  hunk.
- Mark boring patch entries `"trivial": true`; they render collapsed and need
  no comment.
- Use `{"header": "## ..."}` entries as markdown section headings.
- Comments are markdown. Explain why and what to notice rather than restating
  the diff; one to three sentences is usually enough.
- `intro` should state the problem, the approach, and the map of the tour.
- Amending changes the commit hash; re-attach the tour afterward.
