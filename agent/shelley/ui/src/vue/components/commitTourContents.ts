import type { GitTour, GitTourEntry, GitTourHeaderEntry } from "../../services/api";
import { analyzeTourPatch } from "./commitTourPatch";

export const TOUR_OVERVIEW_ANCHOR = "tour-overview";

export interface TourContentsItem {
  anchor: string;
  label: string;
  kind: "overview" | "section" | "change";
  nested?: boolean;
}

export function tourEntryAnchor(position: number): string {
  return `tour-entry-${position}`;
}

function isHeaderEntry(entry: GitTourEntry): entry is GitTourHeaderEntry {
  return "header" in entry;
}

function headerLabel(markdown: string): string {
  const firstLine = markdown
    .split(/\r?\n/)
    .map((line) => line.trim())
    .find(Boolean);
  if (!firstLine) return "Section";
  return firstLine
    .replace(/^#{1,6}\s*/, "")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/[*_`~]/g, "")
    .trim();
}

export function buildTourContents(tour: GitTour, includeOverview: boolean): TourContentsItem[] {
  const contents: TourContentsItem[] = [];
  if (includeOverview || tour.title || tour.intro) {
    contents.push({ anchor: TOUR_OVERVIEW_ANCHOR, label: "Overview", kind: "overview" });
  }

  let inSection = false;
  tour.chunks.forEach((entry, position) => {
    if (isHeaderEntry(entry)) {
      inSection = true;
      contents.push({
        anchor: tourEntryAnchor(position),
        label: headerLabel(entry.header),
        kind: "section",
      });
      return;
    }
    contents.push({
      anchor: tourEntryAnchor(position),
      label: analyzeTourPatch(entry.patch).label,
      kind: "change",
      nested: inSection,
    });
  });
  return contents;
}
