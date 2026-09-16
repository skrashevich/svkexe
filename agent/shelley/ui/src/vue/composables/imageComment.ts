// Click-to-comment on images.
//
// Any image rendered in a conversation (screenshots, read_image, llm_one_shot
// attachments, markdown images) opens the annotation view when clicked. The
// view is rendered once by ChatInterface, so images anywhere in the message
// tree can request it without threading a callback through every component in
// between; this module holds the one open target.
//
// Modifier-clicks keep their native meaning (open the image in a new tab).
import { readonly, ref, type Ref } from "vue";
import { handleModifiedNavClick } from "../utils/openInNewTab";

export interface ImageCommentTarget {
  /** URL the browser can load (what the <img> src is). */
  src: string;
  /** Filesystem path, when the UI knows one. Preferred in comment headers. */
  path?: string;
  /**
   * Dimensions of the file at `path`, when it differs from the rendered
   * image. Tool images are downscaled to fit model limits while `path` points
   * at the full-size original, so regions have to be reported in the
   * original's pixels for a crop of them to mean anything.
   */
  size?: { width: number; height: number };
  /**
   * Whether the file at `path` has an EXIF orientation, meaning its stored
   * pixels are rotated relative to `size` and to what the user is looking at.
   * Carried into the comment so a crop is told to auto-orient first.
   */
  needsAutoOrient?: boolean;
}

const target = ref<ImageCommentTarget | null>(null);

/** The image currently being annotated, if any. */
export function useImageCommentTarget(): Readonly<Ref<ImageCommentTarget | null>> {
  return readonly(target);
}

export function openImageComment(t: ImageCommentTarget): void {
  target.value = t;
}

export function closeImageComment(): void {
  target.value = null;
}

/**
 * Click handler for a commentable image. Modifier-clicks fall through to
 * opening the image in a new tab (the enclosing <a href> would do the same);
 * a plain click opens the annotation view instead of navigating.
 */
export function handleImageCommentClick(e: MouseEvent, t: ImageCommentTarget): void {
  if (handleModifiedNavClick(e, t.src)) return;
  e.preventDefault();
  openImageComment(t);
}
