<template>
  <div ref="viewRef" class="commit-tour-view">
    <article class="commit-tour-document">
      <div v-if="!isMobile" class="commit-tour-toolbar">
        <button
          v-tooltip.top="sideBySide ? 'Switch to unified diffs' : 'Switch to side-by-side diffs'"
          type="button"
          class="commit-tour-diff-toggle"
          :aria-label="sideBySide ? 'Switch to unified diffs' : 'Switch to side-by-side diffs'"
          @click="setSideBySidePreference(!sideBySidePreference)"
        >
          <svg
            width="14"
            height="14"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <rect x="3" y="4" width="18" height="16" rx="2" />
            <path v-if="sideBySide" d="M12 4v16" />
            <path v-else d="M3 9.3h18M3 14.7h18" />
          </svg>
          {{ sideBySide ? "Side-by-side" : "Unified" }}
        </button>
      </div>

      <div
        v-if="commitMessage || tour.tour.title || tour.tour.intro"
        :id="TOUR_OVERVIEW_ANCHOR"
        :data-tour-anchor="TOUR_OVERVIEW_ANCHOR"
        class="commit-tour-overview"
      >
        <section v-if="commitMessage" class="commit-tour-commit-message">
          <div class="commit-tour-commit-meta">
            <code :title="commitMessage.hash">{{ commitMessage.hash.slice(0, 8) }}</code>
            <span>{{ commitMessage.author }}</span>
          </div>
          <h2>{{ commitMessage.subject }}</h2>
          <details v-if="commitMessage.body.trim()" open class="commit-tour-commit-body">
            <summary>
              <span class="commit-tour-commit-chevron" aria-hidden="true">›</span>
              Full message
            </summary>
            <pre>{{ commitMessage.body }}</pre>
          </details>
        </section>

        <header v-if="tour.tour.title || tour.tour.intro" class="commit-tour-introduction">
          <h1 v-if="tour.tour.title">{{ tour.tour.title }}</h1>
          <MarkdownContent v-if="tour.tour.intro" :text="tour.tour.intro" />
        </header>
      </div>

      <template v-for="(entry, position) in tour.tour.chunks" :key="entryKey(entry, position)">
        <MarkdownContent
          v-if="isHeaderEntry(entry)"
          :id="tourEntryAnchor(position)"
          class="commit-tour-section-heading"
          :data-tour-anchor="tourEntryAnchor(position)"
          :text="entry.header"
        />
        <CommitTourChunk
          v-else
          :id="tourEntryAnchor(position)"
          :data-tour-anchor="tourEntryAnchor(position)"
          :entry="entry"
          :theme-type="themeType"
          :side-by-side="sideBySide"
          :overflow="isMobile ? 'wrap' : 'scroll'"
          @comment="emit('open-comment', $event)"
          @line-comment="emit('open-comment', $event)"
        />
      </template>
    </article>

    <button
      v-if="selectionPrompt"
      v-tooltip.top="'Add comment on selection'"
      type="button"
      class="diff-viewer-comment-prompt"
      :style="{
        position: 'fixed',
        top: `${selectionPrompt.top}px`,
        left: `${selectionPrompt.left}px`,
      }"
      @mousedown.prevent
      @click="openSelectionComment"
    >
      💬 Comment
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import type { ThemeTypes } from "@pierre/diffs";
import type { GitTourEntry, GitTourHeaderEntry, GitTourResponse } from "../../services/api";
import type { GitCommitMessage } from "../../types";
import { isDarkModeActive } from "../../services/theme";
import { useSideBySidePreference } from "../composables/diffViewPreference";
import type { TourCommentTarget } from "../composables/tourComments";
import CommitTourChunk from "./CommitTourChunk.vue";
import MarkdownContent from "./MarkdownContent.vue";
import { TOUR_OVERVIEW_ANCHOR, tourEntryAnchor } from "./commitTourContents";

const props = defineProps<{
  tour: GitTourResponse;
  commitMessage: GitCommitMessage | null;
}>();
const emit = defineEmits<{ (e: "open-comment", target: TourCommentTarget): void }>();

const themeType = ref<ThemeTypes>(isDarkModeActive() ? "dark" : "light");
const isMobile = ref(window.innerWidth < 768);
const { sideBySidePreference, setSideBySidePreference } = useSideBySidePreference();
const sideBySide = computed(() => !isMobile.value && sideBySidePreference.value);
const shortHash = computed(() => props.tour.hash.slice(0, 8));
const viewRef = ref<HTMLElement | null>(null);
const selectionPrompt = ref<{
  top: number;
  left: number;
  target: TourCommentTarget;
} | null>(null);
let themeObserver: MutationObserver | null = null;
let selectionFrame: number | null = null;

function isHeaderEntry(entry: GitTourEntry): entry is GitTourHeaderEntry {
  return "header" in entry;
}

function entryKey(entry: GitTourEntry, position: number): string {
  return `${isHeaderEntry(entry) ? "header" : "patch"}-${position}`;
}

function composedClosest(node: Node | null, selector: string): HTMLElement | null {
  let current: Node | null = node;
  while (current) {
    if (current instanceof HTMLElement && current.matches(selector)) return current;
    if (current.parentNode) {
      current = current.parentNode;
      continue;
    }
    const root = current.getRootNode();
    current = root instanceof ShadowRoot ? root.host : null;
  }
  return null;
}

function updateSelectionPrompt() {
  selectionFrame = null;
  const selection = window.getSelection();
  const selectedText = selection?.toString().trim() ?? "";
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed || !selectedText) {
    selectionPrompt.value = null;
    return;
  }
  const selectedInView = composedClosest(selection.anchorNode, ".commit-tour-view");
  if (selectedInView !== viewRef.value) {
    selectionPrompt.value = null;
    return;
  }

  const rect = selection.getRangeAt(0).getBoundingClientRect();
  const chunkElement = composedClosest(selection.anchorNode, "[data-tour-file]");
  const file = chunkElement?.dataset.tourFile;
  const reference = file ? `${file} (${shortHash.value})` : `commit ${shortHash.value}`;
  selectionPrompt.value = {
    top: Math.max(8, Math.min(rect.bottom + 8, window.innerHeight - 40)),
    left: Math.max(8, Math.min(rect.right + 8, window.innerWidth - 120)),
    target: {
      where: file ? `${file} (${shortHash.value})` : `commit ${shortHash.value} narrative`,
      reference,
      selectedText,
    },
  };
}

function handleSelectionChange() {
  if (selectionFrame !== null) cancelAnimationFrame(selectionFrame);
  selectionFrame = requestAnimationFrame(updateSelectionPrompt);
}

function openSelectionComment() {
  if (!selectionPrompt.value) return;
  emit("open-comment", selectionPrompt.value.target);
  selectionPrompt.value = null;
}

function scrollToAnchor(anchor: string) {
  viewRef.value?.querySelector<HTMLElement>(`#${anchor}`)?.scrollIntoView({ block: "start" });
}

defineExpose({ scrollToAnchor });

function handleResize() {
  isMobile.value = window.innerWidth < 768;
}

onMounted(() => {
  themeObserver = new MutationObserver((mutations) => {
    if (mutations.some((mutation) => mutation.attributeName === "class")) {
      themeType.value = isDarkModeActive() ? "dark" : "light";
    }
  });
  themeObserver.observe(document.documentElement, { attributes: true });
  document.addEventListener("selectionchange", handleSelectionChange);
  window.addEventListener("resize", handleResize);
});

onUnmounted(() => {
  themeObserver?.disconnect();
  document.removeEventListener("selectionchange", handleSelectionChange);
  window.removeEventListener("resize", handleResize);
  if (selectionFrame !== null) cancelAnimationFrame(selectionFrame);
});
</script>

<style scoped>
.commit-tour-view {
  width: 100%;
  height: 100%;
  overflow: auto;
  background: var(--bg-base);
  color: var(--text-primary);
}

.commit-tour-document {
  min-width: 0;
  width: min(100%, 1100px);
  margin: 0 auto;
  padding: 1rem clamp(1rem, 3vw, 2.5rem) 4rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.commit-tour-toolbar {
  display: flex;
  justify-content: flex-end;
}

.commit-tour-diff-toggle {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.3rem 0.55rem;
  border: 1px solid var(--border-color);
  border-radius: 0.375rem;
  background: var(--bg-secondary);
  color: var(--text-secondary);
  font: inherit;
  font-size: 0.75rem;
  cursor: pointer;
}

.commit-tour-diff-toggle:hover {
  background: var(--bg-tertiary);
  color: var(--text-primary);
}

.commit-tour-overview {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  scroll-margin-top: 1rem;
}

.commit-tour-commit-message {
  padding: 0.875rem 1rem;
  border: 1px solid color-mix(in srgb, var(--border-color) 75%, var(--accent-color, #3b82f6));
  border-radius: 0.5rem;
  background: color-mix(in srgb, var(--bg-secondary) 88%, var(--accent-color, #3b82f6));
}

.commit-tour-commit-meta {
  display: flex;
  align-items: baseline;
  gap: 0.625rem;
  color: var(--text-secondary);
  font-size: 0.75rem;
}

.commit-tour-commit-meta code {
  flex: 0 0 auto;
  color: var(--text-primary);
  font-family: var(--font-mono, monospace);
  font-weight: 600;
}

.commit-tour-commit-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.commit-tour-commit-message h2 {
  margin: 0.375rem 0 0;
  overflow-wrap: anywhere;
  font-size: clamp(1rem, 2vw, 1.25rem);
  line-height: 1.35;
}

.commit-tour-commit-body {
  min-width: 0;
  margin-top: 0.625rem;
  border-top: 1px solid var(--border-color);
  padding-top: 0.5rem;
}

.commit-tour-commit-body summary {
  width: fit-content;
  display: flex;
  align-items: center;
  gap: 0.25rem;
  color: var(--text-secondary);
  font-size: 0.75rem;
  cursor: pointer;
  list-style: none;
}

.commit-tour-commit-body summary::-webkit-details-marker {
  display: none;
}

.commit-tour-commit-chevron {
  display: inline-block;
  font-size: 1rem;
  line-height: 1;
  transition: transform 0.15s ease;
}

.commit-tour-commit-body[open] .commit-tour-commit-chevron {
  transform: rotate(90deg);
}

.commit-tour-commit-body pre {
  margin: 0.625rem 0 0;
  overflow-x: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: var(--text-primary);
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
  line-height: 1.5;
}

.commit-tour-introduction,
.commit-tour-section-heading,
.commit-tour-chunk {
  scroll-margin-top: 1rem;
}

.commit-tour-introduction,
.commit-tour-section-heading {
  min-width: 0;
  overflow-wrap: anywhere;
}

.commit-tour-introduction {
  padding-bottom: 0.5rem;
}

.commit-tour-introduction h1 {
  margin: 0 0 0.75rem;
  font-size: clamp(1.5rem, 3vw, 2.125rem);
  line-height: 1.2;
}

.commit-tour-introduction :deep(.markdown-content > :first-child),
.commit-tour-section-heading :deep(> :first-child) {
  margin-top: 0;
}

.commit-tour-introduction :deep(.markdown-content > :last-child),
.commit-tour-section-heading :deep(> :last-child) {
  margin-bottom: 0;
}

.commit-tour-section-heading {
  margin-top: 0.75rem;
}

@media (max-width: 767px) {
  .commit-tour-document {
    padding: 1.25rem 0 5rem;
    gap: 0.875rem;
  }

  .commit-tour-commit-message,
  .commit-tour-introduction,
  .commit-tour-section-heading {
    margin-right: 0.875rem;
    margin-left: 0.875rem;
  }
}
</style>
