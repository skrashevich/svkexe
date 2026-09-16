<template>
  <article class="commit-tour-chunk" :data-tour-file="fileLabel">
    <div class="commit-tour-chunk-header">
      <button
        v-if="entry.trivial"
        type="button"
        class="commit-tour-chunk-toggle"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <ToolChevron :expanded="expanded" />
        <code :title="chunkLabel">{{ chunkLabel }}</code>
        <span class="commit-tour-chunk-stats">
          <span class="commit-tour-additions">+{{ chunkStats.additions }}</span>
          <span class="commit-tour-deletions">−{{ chunkStats.deletions }}</span>
        </span>
      </button>
      <template v-else>
        <code :title="chunkLabel">{{ chunkLabel }}</code>
        <span class="commit-tour-chunk-stats">
          <span class="commit-tour-additions">+{{ chunkStats.additions }}</span>
          <span class="commit-tour-deletions">−{{ chunkStats.deletions }}</span>
        </span>
      </template>
      <button
        v-tooltip.top="'Comment on this chunk'"
        type="button"
        class="commit-tour-comment-btn"
        :aria-label="`Comment on ${fileLabel} chunk`"
        @click="openChunkComment"
      >
        💬
      </button>
    </div>

    <div v-if="expanded" class="commit-tour-chunk-body">
      <MarkdownContent v-if="entry.comment" class="commit-tour-comment" :text="entry.comment" />
      <div v-if="isBinary" class="commit-tour-placeholder">
        <span>Binary file</span>
        <pre>{{ entry.patch }}</pre>
      </div>
      <div v-else-if="!isHunk" class="commit-tour-placeholder">
        <pre>{{ entry.patch || "No textual diff." }}</pre>
      </div>
      <template v-else>
        <div
          ref="diffHostEl"
          class="commit-tour-diff-host"
          :style="rendered ? undefined : { minHeight: placeholderHeight }"
          @pointerdown.capture="rememberPointerDown"
        ></div>
        <pre v-if="diffError" class="commit-tour-raw-diff">{{ entry.patch }}</pre>
      </template>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { FileDiffMetadata, FileDiffOptions, ThemeTypes, ThemesType } from "@pierre/diffs";
import { getSingularPatch } from "@pierre/diffs";
import type { GitTourPatchEntry } from "../../services/api";
import { useFileDiffInstance } from "../composables/fileDiffInstance";
import {
  patchLineText,
  type TourCommentTarget,
  type TourDiffSide,
} from "../composables/tourComments";
import { useNearViewport } from "../composables/nearViewport";
import { analyzeTourPatch } from "./commitTourPatch";
import MarkdownContent from "./MarkdownContent.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{
  entry: GitTourPatchEntry;
  themeType: ThemeTypes;
  sideBySide: boolean;
  overflow: "scroll" | "wrap";
}>();
const emit = defineEmits<{
  (e: "comment", target: TourCommentTarget): void;
  (e: "line-comment", target: TourCommentTarget): void;
}>();

const DIFF_THEMES: ThemesType = { dark: "github-dark", light: "github-light" };
const MAX_CLICK_MOVEMENT_SQUARED = 25;
const expanded = ref(!props.entry.trivial);
const diffHostEl = ref<HTMLElement | null>(null);
const nearViewport = useNearViewport(diffHostEl);
let pointerDownPos: { x: number; y: number } | null = null;

watch(
  () => [props.entry.patch, props.entry.trivial] as const,
  () => {
    expanded.value = !props.entry.trivial;
  },
);

const patchInfo = computed(() => analyzeTourPatch(props.entry.patch));
const paths = computed(() => ({
  old: patchInfo.value.oldPath,
  new: patchInfo.value.newPath,
  newFile: patchInfo.value.newFile,
  deletedFile: patchInfo.value.deletedFile,
  label: patchInfo.value.fileLabel,
}));
const fileLabel = computed(() => patchInfo.value.fileLabel);
const hunkRanges = computed(() => patchInfo.value.hunkRanges);
const chunkLabel = computed(() => patchInfo.value.label);
const chunkStats = computed(() => ({
  additions: patchInfo.value.additions,
  deletions: patchInfo.value.deletions,
}));
const isHunk = computed(() => patchInfo.value.isHunk);
const isBinary = computed(() => patchInfo.value.isBinary);

const fileDiff = computed<FileDiffMetadata | null>(() => {
  if (!expanded.value || !nearViewport.value || !isHunk.value) return null;
  try {
    return getSingularPatch(props.entry.patch);
  } catch (error) {
    console.warn("Commit tour diff parse error:", error);
    return null;
  }
});

function rememberPointerDown(event: PointerEvent) {
  if (!event.isPrimary || event.button !== 0) {
    pointerDownPos = null;
    return;
  }
  pointerDownPos = { x: event.clientX, y: event.clientY };
}

function isPlainLineClick(event: PointerEvent): boolean {
  const start = pointerDownPos;
  pointerDownPos = null;
  if (start) {
    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    if (dx * dx + dy * dy > MAX_CLICK_MOVEMENT_SQUARED) return false;
  }
  return window.getSelection()?.isCollapsed !== false;
}

function openChunkComment() {
  const hunk = hunkRanges.value[0]?.line || "file change";
  emit("comment", {
    where: `${fileLabel.value} chunk`,
    reference: `${fileLabel.value} (${hunk})`,
  });
}

function openLineComment(side: TourDiffSide, lineNumber: number) {
  const selectedText = patchLineText(props.entry.patch, side, lineNumber);
  if (selectedText === null) return;
  const path = (side === "deletions" ? paths.value.old : paths.value.new) || fileLabel.value;
  const sideLabel = side === "deletions" ? "old" : "new";
  emit("line-comment", {
    where: `${path} line ${lineNumber} (${sideLabel})`,
    reference: `${path}:${lineNumber}`,
    selectedText,
    quoteCode: true,
  });
}

const diffOptions = computed<FileDiffOptions<undefined>>(() => ({
  diffStyle:
    props.sideBySide && !paths.value.newFile && !paths.value.deletedFile ? "split" : "unified",
  theme: DIFF_THEMES,
  themeType: props.themeType,
  disableFileHeader: true,
  overflow: props.overflow,
  lineHoverHighlight: "line",
  onLineClick: ({ annotationSide, lineNumber, event }) => {
    if (isPlainLineClick(event)) openLineComment(annotationSide, lineNumber);
  },
  onLineNumberClick: ({ annotationSide, lineNumber }) => {
    pointerDownPos = null;
    openLineComment(annotationSide, lineNumber);
  },
}));

const placeholderHeight = computed(
  () => `${Math.min(Math.max(props.entry.patch.split("\n").length, 4) * 20, 1600)}px`,
);
const diffError = computed(
  () => expanded.value && nearViewport.value && isHunk.value && fileDiff.value == null,
);

const { rendered } = useFileDiffInstance(diffHostEl, () => {
  if (!fileDiff.value) return null;
  return { fileDiff: fileDiff.value, options: diffOptions.value };
});
</script>

<style scoped>
.commit-tour-chunk {
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: 0.5rem;
  background: var(--bg-base);
}

.commit-tour-chunk-header {
  min-height: 2.5rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--bg-secondary);
  color: var(--text-primary);
}

.commit-tour-chunk-toggle {
  min-width: 0;
  flex: 1;
  align-self: stretch;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: -0.5rem 0 -0.5rem -0.75rem;
  padding: 0.5rem 0.75rem;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.commit-tour-chunk-toggle:hover {
  background: var(--bg-tertiary);
}

.commit-tour-comment-btn {
  flex: 0 0 auto;
  width: 1.75rem;
  height: 1.75rem;
  padding: 0;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  opacity: 0;
  cursor: pointer;
}

.commit-tour-chunk-header:hover .commit-tour-comment-btn,
.commit-tour-comment-btn:focus-visible {
  opacity: 1;
}

.commit-tour-comment-btn:hover {
  background: var(--bg-tertiary);
}

.commit-tour-chunk-header code {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
  font-size: 0.8125rem;
}

.commit-tour-chunk-stats {
  display: inline-flex;
  gap: 0.375rem;
  margin-left: auto;
  flex: 0 0 auto;
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
  font-weight: 600;
}

.commit-tour-additions {
  color: var(--green-text, #16a34a);
}

.commit-tour-deletions {
  color: var(--red-text, #dc2626);
}

.commit-tour-chunk-body {
  min-width: 0;
}

.commit-tour-comment {
  padding: 0.875rem 1rem;
  border-top: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
  background: var(--bg-secondary);
}

.commit-tour-comment :deep(> :first-child) {
  margin-top: 0;
}

.commit-tour-comment :deep(> :last-child) {
  margin-bottom: 0;
}

.commit-tour-diff-host,
.commit-tour-diff-host :deep(diffs-container) {
  display: block;
  min-width: 0;
}

.commit-tour-diff-host :deep(diffs-container) {
  contain: content;
}

.commit-tour-placeholder,
.commit-tour-raw-diff {
  margin: 0;
  padding: 0.875rem 1rem;
  color: var(--text-secondary);
  background: var(--bg-base);
  font-size: 0.75rem;
}

.commit-tour-placeholder > span {
  display: block;
  margin-bottom: 0.5rem;
  font-weight: 600;
}

.commit-tour-placeholder pre,
.commit-tour-raw-diff {
  margin: 0;
  overflow-x: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--font-mono, monospace);
}

@media (max-width: 767px) {
  .commit-tour-chunk {
    border-right: 0;
    border-left: 0;
    border-radius: 0;
  }

  .commit-tour-chunk-header {
    padding-right: 0.625rem;
    padding-left: 0.625rem;
  }

  .commit-tour-comment-btn {
    opacity: 1;
  }

  .commit-tour-chunk-toggle {
    margin-left: -0.625rem;
    padding-left: 0.625rem;
  }

  .commit-tour-comment,
  .commit-tour-placeholder,
  .commit-tour-raw-diff {
    padding: 0.75rem;
  }
}
</style>
