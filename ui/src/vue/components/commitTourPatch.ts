export interface TourHunkRange {
  line: string;
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
}

export interface TourPatchInfo {
  oldPath: string | null;
  newPath: string | null;
  newFile: boolean;
  deletedFile: boolean;
  fileLabel: string;
  hunkRanges: TourHunkRange[];
  displayRange: [number, number] | null;
  label: string;
  additions: number;
  deletions: number;
  isHunk: boolean;
  isBinary: boolean;
}

const HUNK_HEADER = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

interface MarkerPath {
  present: boolean;
  path: string | null;
}

function unquoteGitPath(path: string): string {
  if (!path.startsWith('"') || !path.endsWith('"')) return path;
  const inner = path.slice(1, -1);
  const encoder = new TextEncoder();
  const bytes: number[] = [];
  let i = 0;
  while (i < inner.length) {
    const backslash = inner.indexOf("\\", i);
    if (backslash === -1) {
      bytes.push(...encoder.encode(inner.slice(i)));
      break;
    }
    if (backslash > i) bytes.push(...encoder.encode(inner.slice(i, backslash)));
    const next = inner[backslash + 1];
    if (next >= "0" && next <= "7") {
      bytes.push(parseInt(inner.slice(backslash + 1, backslash + 4), 8));
      i = backslash + 4;
    } else {
      const escapes: Record<string, number> = {
        a: 7,
        b: 8,
        t: 9,
        n: 10,
        v: 11,
        f: 12,
        r: 13,
        '"': 34,
        "\\": 92,
      };
      const code = escapes[next];
      if (code === undefined) bytes.push(...encoder.encode(next));
      else bytes.push(code);
      i = backslash + 2;
    }
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}

function markerPath(patch: string, marker: "--- " | "+++ "): MarkerPath {
  let line: string | undefined;
  for (const candidate of patch.split("\n")) {
    if (HUNK_HEADER.test(candidate)) break;
    if (candidate.startsWith(marker)) line = candidate;
  }
  if (!line) return { present: false, path: null };

  let path = unquoteGitPath(line.slice(marker.length).trim());
  if (path === "/dev/null") return { present: true, path: null };
  if (path.startsWith("a/") || path.startsWith("b/")) path = path.slice(2);
  return { present: true, path };
}

function hunklessPath(patch: string): string | null {
  for (const line of patch.split("\n")) {
    if (line.startsWith("rename to ")) return unquoteGitPath(line.slice("rename to ".length));
  }
  const quoted = /^diff --git "a\/(.*)" "b\/(.*)"$/m.exec(patch);
  if (quoted) return unquoteGitPath(`"${quoted[2]}"`);
  const git = /^diff --git a\/(.*) b\/(.*)$/m.exec(patch);
  if (!git) return null;
  if (git[1] === git[2]) return git[1];
  const combined = `${git[1]} b/${git[2]}`;
  const path = combined.slice(0, (combined.length - 3) / 2);
  return combined === `${path} b/${path}` ? path : git[2];
}

export function analyzeTourPatch(patch: string): TourPatchInfo {
  const oldMarker = markerPath(patch, "--- ");
  const newMarker = markerPath(patch, "+++ ");
  const oldPath = oldMarker.path;
  const newPath = newMarker.path;
  const fileLabel = newPath || oldPath || hunklessPath(patch) || "File change";

  const hunkRanges: TourHunkRange[] = [];
  let additions = 0;
  let deletions = 0;
  let inHunk = false;
  for (const line of patch.split("\n")) {
    const match = HUNK_HEADER.exec(line);
    if (match) {
      inHunk = true;
      hunkRanges.push({
        line,
        oldStart: Number(match[1]),
        oldCount: match[2] === undefined ? 1 : Number(match[2]),
        newStart: Number(match[3]),
        newCount: match[4] === undefined ? 1 : Number(match[4]),
      });
      continue;
    }
    if (!inHunk) continue;
    if (line.startsWith("+")) additions++;
    else if (line.startsWith("-")) deletions++;
  }

  let displayRange: [number, number] | null = null;
  if (hunkRanges.length > 0) {
    const useOldSide = hunkRanges.every((range) => range.newCount === 0);
    const first = hunkRanges[0];
    const last = hunkRanges[hunkRanges.length - 1];
    const start = useOldSide ? first.oldStart : first.newStart;
    const lastStart = useOldSide ? last.oldStart : last.newStart;
    const lastCount = useOldSide ? last.oldCount : last.newCount;
    displayRange = [start, lastStart + Math.max(lastCount, 1) - 1];
  }

  return {
    oldPath,
    newPath,
    newFile: oldMarker.present && oldPath === null,
    deletedFile: newMarker.present && newPath === null,
    fileLabel,
    hunkRanges,
    displayRange,
    label: displayRange ? `${fileLabel} · lines ${displayRange[0]}–${displayRange[1]}` : fileLabel,
    additions,
    deletions,
    isHunk: hunkRanges.length > 0,
    isBinary: /^(?:GIT binary patch|Binary files .* differ)$/m.test(patch),
  };
}
