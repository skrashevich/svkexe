// Comment blocks for image annotations, the image-side analogue of
// utils/messageQuote.ts and the diff viewer's line comments.
//
// A user clicks an image in the conversation, drags a box around the part they
// want to talk about, and types a comment. What lands in the message input is a
// quoted header identifying the image and the region, followed by the comment:
//
//   > image /tmp/shot.png [region 300x180+120+340 of 1280x800]
//   the login button is cut off here
//
// The region is an ImageMagick geometry (WxH+X+Y) so the agent can crop exactly
// what the user circled (`magick shot.png -crop 300x180+120+340 crop.png`).

/** A region of an image, in whole pixels of some coordinate space. */
export interface ImageRegion {
  x: number;
  y: number;
  width: number;
  height: number;
}

/**
 * A box the user dragged, as fractions (0..1) of the displayed image. Kept
 * resolution-independent so it can be resolved into any coordinate space with a
 * single rounding step; rounding in display pixels and scaling afterwards
 * compounds the error and shifts the crop.
 */
export interface ImageBox {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

export interface ImageAnnotation {
  /** Box the user marked, or undefined for a comment on the whole image. */
  box?: ImageBox;
  text: string;
}

/** Intrinsic pixel dimensions of the annotated image. */
export interface ImageSize {
  width: number;
  height: number;
}

/**
 * Resolve a dragged box into whole pixels of `size`.
 *
 * `size` is the source file's, not the rendered image's: tool images are
 * downscaled to fit model limits while the comment header names the full-size
 * original on disk. Clamped inside the image and never empty, so the geometry
 * is always a crop ImageMagick will accept.
 */
export function regionIn(box: ImageBox, size: ImageSize): ImageRegion {
  const x = clamp(Math.round(box.left * size.width), 0, Math.max(size.width - 1, 0));
  const y = clamp(Math.round(box.top * size.height), 0, Math.max(size.height - 1, 0));
  return {
    x,
    y,
    width: clamp(Math.round(box.right * size.width) - x, 1, size.width - x),
    height: clamp(Math.round(box.bottom * size.height) - y, 1, size.height - y),
  };
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi);
}

/**
 * How to refer to an image in a comment header. A filesystem path is best (the
 * agent can open it); otherwise we fall back to the URL the browser loaded.
 *
 * Both the per-message file endpoint (markdown images) and /api/read (the
 * screenshot tool's Display.url) carry the real path in a `path` query
 * parameter, so recover it when present. Only same-origin API URLs are trusted
 * that way: a remote image whose URL happens to contain `path=` names nothing
 * on this machine, and quoting it as a path would send the agent hunting for a
 * file that does not exist.
 */
export function imageRefFromSrc(src: string, path?: string): string {
  if (path) return path;
  // A data: URI is the image itself; quoting megabytes of base64 into the
  // message input would be useless (and expensive), so name it instead.
  if (/^data:/i.test(src)) return "(inline image)";
  const url = sameOriginURL(src);
  const fromQuery = url?.pathname.startsWith("/api/") ? url.searchParams.get("path") : null;
  return fromQuery || src;
}

function sameOriginURL(src: string): URL | null {
  // A placeholder base resolves relative URLs (which are same-origin by
  // definition) and lets this run outside a browser, e.g. under the unit tests.
  const base = typeof window === "undefined" ? "http://localhost/" : window.location.href;
  try {
    const url = new URL(src, base);
    return url.origin === new URL(base).origin ? url : null;
  } catch {
    return null;
  }
}

/** ImageMagick-style geometry for a region: WxH+X+Y. */
export function regionGeometry(r: ImageRegion): string {
  return `${r.width}x${r.height}+${r.x}+${r.y}`;
}

/**
 * Dimensions of the source file a tool's Display refers to (`source_width` /
 * `source_height`), or undefined when the tool didn't report them. Already
 * corrected for EXIF orientation by the tool, so they describe the file the way
 * it renders.
 */
export function displaySourceSize(display: unknown): ImageSize | undefined {
  if (typeof display !== "object" || display === null) return undefined;
  const d = display as Record<string, unknown>;
  const width = d.source_width;
  const height = d.source_height;
  if (typeof width !== "number" || typeof height !== "number") return undefined;
  if (width <= 0 || height <= 0) return undefined;
  return { width, height };
}

/**
 * Whether the source file needs auto-orienting before its pixels line up with
 * the dimensions above (`source_orientation`, absent when it doesn't).
 *
 * Set only for EXIF-rotated images. Viewers apply the tag; tools that crop, and
 * `magick` among them, read stored pixels and ignore it. A region measured
 * against what the user saw therefore has to come with the instruction to
 * auto-orient first, or the crop lands somewhere else in the file.
 */
export function displayNeedsAutoOrient(display: unknown): boolean {
  if (typeof display !== "object" || display === null) return false;
  const o = (display as Record<string, unknown>).source_orientation;
  return typeof o === "number" && o > 1;
}

/** Header line (without the leading "> ") describing what was annotated. */
export function imageCommentHeader(
  ref: string,
  size: ImageSize,
  region: ImageRegion | undefined,
  needsAutoOrient = false,
): string {
  const where = region
    ? `region ${regionGeometry(region)} of ${size.width}x${size.height}`
    : `whole image, ${size.width}x${size.height}`;
  // Said in the header rather than left implicit: the coordinates are in the
  // image as displayed, and a crop of the stored pixels would disagree.
  const orient = needsAutoOrient ? ", auto-orient first" : "";
  return `image ${ref} [${where}${orient}]`;
}

/**
 * Render annotations as quoted comment blocks for the message input. Boxes are
 * resolved into `size`'s pixels here, the one place rounding happens. The
 * trailing blank line puts the composer cursor below the last comment, matching
 * buildMessageQuote and the diff viewer's comment blocks.
 */
export function buildImageCommentBlocks(
  ref: string,
  size: ImageSize,
  annotations: ImageAnnotation[],
  needsAutoOrient = false,
): string {
  const blocks: string[] = [];
  for (const a of annotations) {
    const text = a.text.trim();
    if (!text) continue;
    const region = a.box && regionIn(a.box, size);
    blocks.push(`> ${imageCommentHeader(ref, size, region, needsAutoOrient)}\n${text}\n`);
  }
  return blocks.length === 0 ? "" : blocks.join("\n") + "\n";
}
