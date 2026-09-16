<!-- An image in the conversation that can be commented on. A plain click opens
     the annotation view (ImageCommentModal, hosted by ChatInterface); a
     modifier-click keeps the browser's "open in new tab" behavior, which is
     why this is a real link rather than a button: cmd-clicking an image to
     pop it out is muscle memory, and only an anchor gives that for free.
     Used by every tool card that renders an image so the click-to-comment
     affordance is identical everywhere. -->
<template>
  <a
    class="commentable-image-link"
    :href="src"
    target="_blank"
    rel="noopener noreferrer"
    :aria-label="`Comment on ${label}`"
    @click="onClick"
  >
    <img
      :src="src"
      :alt="alt"
      class="tool-image-responsive commentable-image"
      :width="width || undefined"
      :height="height || undefined"
    />
    <!-- Hover/focus affordance: a cursor change alone doesn't tell you an image
         is clickable, let alone that clicking comments on it. -->
    <span class="commentable-image-badge" aria-hidden="true">
      <span v-html="COMMENT_ICON" />
      Comment
    </span>
  </a>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { handleImageCommentClick } from "../composables/imageComment";
import { COMMENT_ICON } from "../../utils/icons";
import { imageRefFromSrc } from "../../utils/imageComment";
import { tildifyPath } from "../../utils/tildify";

const props = defineProps<{
  src: string;
  alt: string;
  /** Filesystem path, when known; preferred over the URL in comment headers. */
  path?: string;
  width?: number;
  height?: number;
  /** Dimensions of the file at `path` when it differs from the rendered image
   *  (i.e. the image was downscaled for the model). */
  sourceWidth?: number;
  sourceHeight?: number;
  /** The file at `path` is EXIF-rotated, so a crop of it must auto-orient. */
  needsAutoOrient?: boolean;
}>();

const label = computed(() => {
  const ref = imageRefFromSrc(props.src, props.path);
  return tildifyPath(ref) || ref;
});

function onClick(e: MouseEvent) {
  handleImageCommentClick(e, {
    src: props.src,
    path: props.path,
    size:
      props.sourceWidth && props.sourceHeight
        ? { width: props.sourceWidth, height: props.sourceHeight }
        : undefined,
    needsAutoOrient: props.needsAutoOrient,
  });
}
</script>
