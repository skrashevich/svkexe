import { truncateWithEllipsis } from "./monacoComments";

export type TourDiffSide = "deletions" | "additions";

export interface TourCommentTarget {
  where: string;
  reference: string;
  selectedText?: string;
  quoteCode?: boolean;
}

export function patchLineText(
  patch: string,
  side: TourDiffSide,
  lineNumber: number,
): string | null {
  let oldLine: number | null = null;
  let newLine: number | null = null;

  for (const line of patch.split("\n")) {
    const header = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (header) {
      oldLine = Number(header[1]);
      newLine = Number(header[2]);
      continue;
    }
    if (oldLine === null || newLine === null) continue;

    const marker = line[0];
    const text = line.slice(1);
    if (marker === " ") {
      if (side === "deletions" && oldLine === lineNumber) return text;
      if (side === "additions" && newLine === lineNumber) return text;
      oldLine++;
      newLine++;
    } else if (marker === "-") {
      if (side === "deletions" && oldLine === lineNumber) return text;
      oldLine++;
    } else if (marker === "+") {
      if (side === "additions" && newLine === lineNumber) return text;
      newLine++;
    }
  }

  return null;
}

export function buildTourCommentBlock(target: TourCommentTarget, comment: string): string {
  const selected = target.selectedText?.split("\n")[0]?.trim() ?? "";
  const quote =
    target.quoteCode || selected
      ? `> ${target.reference}: ${truncateWithEllipsis(selected, 60)}`
      : `> ${target.reference}`;
  return `${quote}\n${comment}\n\n`;
}
