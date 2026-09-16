<!-- Annotation view for a single image: drag a box on the image, type a comment
     for it, and the comment goes straight into the message input as a quoted
     block (the image-side counterpart of the diff viewer's line comments).
     Layered over the conversation on a backdrop, like the diff viewer, so it
     reads as a temporary surface over the message you came from.

     Comments are inserted as you go, one per box, using the same CommentDialog
     as the diff viewer -- there is no staging list to fill in and submit. Boxes
     already commented on stay drawn on the image so you can see what you have
     covered, and are resolved to pixels of the source file, which is larger
     than the rendered image when the image was downscaled for the model. The
     text is built by utils/imageComment.ts. -->
<template>
  <Teleport to="body">
    <!-- No click-to-close on the backdrop: a stray click would discard the
         comment being typed, and the diff viewer doesn't either. -->
    <div class="image-comment-overlay">
      <div
        ref="overlayRef"
        v-focustrap
        class="image-comment-container"
        role="dialog"
        aria-modal="true"
        aria-label="Comment on image"
        tabindex="-1"
        @keydown="onKeyDown"
      >
        <div class="image-comment-bar">
          <span class="image-comment-title" :title="fullLabel">{{ shortLabel }}</span>
          <span v-if="!failed" class="image-comment-hint">{{ hint }}</span>
          <a
            class="image-comment-bar-link"
            :href="target.src"
            target="_blank"
            rel="noopener noreferrer"
            >Open original</a
          >
          <button
            type="button"
            class="image-comment-close"
            aria-label="Close image comments"
            @click="emit('close')"
          >
            ×
          </button>
        </div>

        <div class="image-comment-stage">
          <div v-if="failed" class="image-comment-failed">
            <p>This image could not be loaded, so there is nothing to annotate.</p>
            <p>
              <a :href="target.src" target="_blank" rel="noopener noreferrer">{{ fullLabel }}</a>
            </p>
            <button
              type="button"
              class="diff-viewer-btn diff-viewer-btn-secondary"
              @click="emit('close')"
            >
              Close
            </button>
          </div>
          <div
            v-else
            ref="frameRef"
            class="image-comment-frame"
            :class="{ 'is-drawing': !!drag }"
            @pointerdown="onPointerDown"
            @pointermove="onPointerMove"
            @pointerup="onPointerUp"
            @pointercancel="onPointerCancel"
            @lostpointercapture="onPointerCancel"
          >
            <img
              ref="imgRef"
              class="image-comment-img"
              :src="target.src"
              :alt="fullLabel"
              draggable="false"
              @load="onImageLoad"
              @error="failed = true"
            />
            <!-- Regions already commented on, so you can see what you covered. -->
            <div
              v-for="(r, i) in commented"
              :key="r.id"
              class="image-comment-region"
              :style="boxStyle(r.box)"
              :aria-label="`Comment ${i + 1}, ${whereLabel(r.box)}`"
            >
              <span class="image-comment-region-badge">{{ i + 1 }}</span>
            </div>
            <div v-if="pendingBox" class="image-comment-region active" :style="boxStyle(pendingBox)" />
            <div v-if="dragBox" class="image-comment-draft" :style="boxStyle(dragBox)" />
          </div>
        </div>

        <div v-if="!failed" class="image-comment-foot">
          <button
            type="button"
            class="diff-viewer-btn diff-viewer-btn-secondary"
            :disabled="!loaded"
            @click="openDialog(undefined)"
          >
            Comment on the whole image
          </button>
          <span v-if="sentCount > 0" class="image-comment-sent" role="status">
            {{ sentCount === 1 ? "1 comment added" : `${sentCount} comments added` }} to the message
            input
          </span>
        </div>

        <CommentDialog
          v-if="dialog"
          :key="dialog.opened"
          v-model:text="commentText"
          :where="dialog.box ? whereLabel(dialog.box) : 'whole image'"
          @submit="addComment"
          @cancel="cancelDialog"
        />
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import {
  buildImageCommentBlocks,
  imageRefFromSrc,
  regionGeometry,
  regionIn,
  type ImageBox,
  type ImageSize,
} from "../../utils/imageComment";
import { tildifyPath } from "../../utils/tildify";
import { focusMessageInputIfUnfocused } from "../../utils/focusMessageInput";
import { popModalEscape, pushModalEscape } from "../composables/modalEscapeStack";
import type { ImageCommentTarget } from "../composables/imageComment";
import CommentDialog from "./CommentDialog.vue";

const props = defineProps<{ target: ImageCommentTarget }>();
const emit = defineEmits<{
  (e: "submit", text: string): void;
  (e: "close"): void;
}>();

const overlayRef = ref<HTMLDivElement | null>(null);
const frameRef = ref<HTMLDivElement | null>(null);
const imgRef = ref<HTMLImageElement | null>(null);
// Regions whose comment is already in the message input, kept only to draw them
// and number them.
const commented = ref<{ id: number; box: ImageBox }[]>([]);
// Open dialog's target: a box, or undefined for a whole-image comment. Null when
// no dialog is open. `opened` keys the dialog so retargeting remounts it,
// recentering and refocusing it even when the new region's label matches.
const dialog = ref<{ box?: ImageBox; opened: number } | null>(null);
const commentText = ref("");
// Intrinsic size of the rendered image, known once it has loaded. Drawing is
// inert until then.
const natural = ref<ImageSize | null>(null);
// Space the comments are written in: the source file's when the tool reported
// it, else the rendered image's. The source dimensions come pre-corrected for
// EXIF orientation, so they describe the file as rendered and differ from
// `natural` only in scale. Either way the header states the dimensions it used,
// so a region is self-describing rather than silently mis-scaled.
const commentSize = computed(() => props.target.size ?? natural.value);
const failed = ref(false);
const loaded = computed(() => natural.value !== null);
const sentCount = ref(0);
const hint = computed(() =>
  loaded.value ? "Drag a box on the image to comment on it" : "Loading image…",
);
// Box awaiting its comment, drawn while the dialog is open so it is clear which
// region the dialog belongs to.
const pendingBox = computed(() => dialog.value?.box ?? null);
// In-progress drag: the frame's rect as it was when the drag began (so a
// scroll or resize mid-drag cannot skew it) plus positions as fractions of it.
const drag = ref<{
  rect: DOMRect;
  startX: number;
  startY: number;
  x: number;
  y: number;
} | null>(null);

let nextId = 1;
// Pointer that started the current drag; a second touch must not hijack it.
let dragPointerId: number | null = null;
// Element focused before the view opened, so closing puts focus back.
let returnFocusTo: HTMLElement | null = null;

const ref_ = computed(() => imageRefFromSrc(props.target.src, props.target.path));
const fullLabel = computed(() => tildifyPath(ref_.value) || ref_.value);
// Paths are long and their tail is the informative part, so the bar shows the
// filename and the tooltip the whole thing. A ref that is a URL rather than a
// path is shown whole; its last segment would be a meaningless id.
const shortLabel = computed(() => {
  const last = fullLabel.value.split("/").pop();
  return last?.includes(".") ? last : fullLabel.value;
});

// The drag rectangle, normalized so dragging in any direction works. Anything
// under a few rendered pixels is a click, not a selection — same slip tolerance
// as the diff viewer's click-to-comment (monacoComments.ts).
const DRAG_SLOP_PX = 4;
const dragBox = computed<ImageBox | null>(() => {
  const d = drag.value;
  if (!d) return null;
  const box = {
    left: Math.min(d.startX, d.x),
    top: Math.min(d.startY, d.y),
    right: Math.max(d.startX, d.x),
    bottom: Math.max(d.startY, d.y),
  };
  const minW = DRAG_SLOP_PX / d.rect.width;
  const minH = DRAG_SLOP_PX / d.rect.height;
  return box.right - box.left < minW || box.bottom - box.top < minH ? null : box;
});

function boxStyle(box: ImageBox): Record<string, string> {
  return {
    left: `${box.left * 100}%`,
    top: `${box.top * 100}%`,
    width: `${(box.right - box.left) * 100}%`,
    height: `${(box.bottom - box.top) * 100}%`,
  };
}

/** A box's geometry as it will be written out: in the coordinates of the file
 * the header names. */
function whereLabel(box: ImageBox): string {
  const size = commentSize.value;
  return size ? regionGeometry(regionIn(box, size)) : "";
}

function onImageLoad() {
  const img = imgRef.value;
  if (!img || !img.naturalWidth || !img.naturalHeight) return;
  natural.value = { width: img.naturalWidth, height: img.naturalHeight };
}

// Convert a pointer position into a fraction of the frame it was measured
// against, clamped so a drag that leaves the frame still yields a valid box.
function toFraction(e: PointerEvent, rect: DOMRect): { x: number; y: number } {
  return {
    x: clamp01((e.clientX - rect.left) / rect.width),
    y: clamp01((e.clientY - rect.top) / rect.height),
  };
}

// The frame shrink-wraps the image (display: inline-block), so its box is the
// image's displayed pixels. Null until the image loads, which is what makes
// drawing inert before then.
function frameRect(): DOMRect | null {
  const frame = frameRef.value;
  if (!frame || !natural.value) return null;
  const rect = frame.getBoundingClientRect();
  return rect.width > 0 && rect.height > 0 ? rect : null;
}

function clamp01(v: number): number {
  return Math.min(Math.max(v, 0), 1);
}

function onPointerDown(e: PointerEvent) {
  if (e.button !== 0 || drag.value) return;
  const rect = frameRect();
  if (!rect) return;
  e.preventDefault();
  dragPointerId = e.pointerId;
  const p = toFraction(e, rect);
  drag.value = { rect, startX: p.x, startY: p.y, x: p.x, y: p.y };
  // Capture so the drag survives the pointer leaving the frame — or the window,
  // where a plain pointerup listener would never fire and would strand it.
  frameRef.value?.setPointerCapture(e.pointerId);
}

function onPointerMove(e: PointerEvent) {
  const d = drag.value;
  if (!d || e.pointerId !== dragPointerId) return;
  drag.value = { ...d, ...toFraction(e, d.rect) };
}

function onPointerUp(e: PointerEvent) {
  if (e.pointerId !== dragPointerId) return;
  // Take the final position from pointerup itself: a fast drag (or a synthetic
  // one) can end without a preceding pointermove at the release point.
  onPointerMove(e);
  const box = dragBox.value;
  endDrag();
  if (box) openDialog(box);
}

// Fires on pointercancel and on any other loss of capture (the element being
// removed, another element capturing). Either way the drag produced nothing.
function onPointerCancel(e: PointerEvent) {
  if (e.pointerId !== dragPointerId) return;
  endDrag();
}

function endDrag() {
  if (dragPointerId !== null && frameRef.value?.hasPointerCapture(dragPointerId)) {
    frameRef.value.releasePointerCapture(dragPointerId);
  }
  drag.value = null;
  dragPointerId = null;
}

let opens = 0;

// Drawing a new box while a comment is half-typed retargets the dialog to the
// new box, matching the diff viewer: the text you have written follows you
// rather than being silently dropped.
function openDialog(box?: ImageBox) {
  dialog.value = { box, opened: ++opens };
}

function cancelDialog() {
  dialog.value = null;
  commentText.value = "";
  overlayRef.value?.focus();
}

function addComment() {
  const d = dialog.value;
  const text = commentText.value.trim();
  if (!d || !text) return;
  const size = commentSize.value;
  // Guaranteed: the dialog can only be opened once the image has loaded.
  if (!size) throw new Error("image comment added before the image loaded");
  emit(
    "submit",
    buildImageCommentBlocks(ref_.value, size, [{ box: d.box, text }], !!props.target.needsAutoOrient),
  );
  sentCount.value += 1;
  if (d.box) commented.value.push({ id: nextId++, box: d.box });
  dialog.value = null;
  commentText.value = "";
  // Keep focus in this dialog: MessageInput focuses itself after an injection,
  // which would otherwise pull focus into the composer behind the still-open
  // view and send the next keystroke to the wrong place.
  overlayRef.value?.focus();
}

// Escape backs out of the drag in progress, then the open comment, then the
// view. Routed through the shared stack so a stacked modal keeps priority.
function requestClose() {
  if (drag.value) {
    endDrag();
    return;
  }
  if (dialog.value) {
    cancelDialog();
    return;
  }
  emit("close");
}

// ⌘/Ctrl+Enter submits the open comment, matching the composer.
function onKeyDown(e: KeyboardEvent) {
  if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && dialog.value && commentText.value.trim()) {
    e.preventDefault();
    addComment();
  }
}

onMounted(() => {
  pushModalEscape(requestClose);
  // Move focus into the view so keyboard users are not left typing into the
  // conversation behind it; restored on close. v-focustrap keeps it here.
  returnFocusTo = document.activeElement as HTMLElement | null;
  overlayRef.value?.focus();
  // A cached (or already-failed) image settles before the listeners attach.
  const img = imgRef.value;
  if (img?.complete) {
    if (img.naturalWidth) onImageLoad();
    else failed.value = true;
  }
});

onBeforeUnmount(() => {
  popModalEscape(requestClose);
  endDrag();
  // Put focus back where it came from, unless that element is gone (a markdown
  // re-render or conversation switch), in which case fall back to the composer
  // rather than dumping focus on <body>.
  if (returnFocusTo?.isConnected) returnFocusTo.focus();
  else focusMessageInputIfUnfocused();
});
</script>
