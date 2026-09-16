import { test, expect, type Locator, type Page } from "@playwright/test";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { deflateSync } from "node:zlib";
import { createConversationViaAPI } from "./helpers";

// Clicking an image in the conversation opens the annotation view: drag a box
// around part of the image, type a comment in the dialog that opens, and the
// comment goes straight into the message input as a quoted block naming the
// image and the region — the image-side counterpart of the diff viewer's line
// comments, using the same CommentDialog and the same insert-as-you-go flow.
//
// The "inline image" predictable pattern writes a 48x48 PNG into the
// conversation cwd via bash and then references it with relative-path
// markdown, so we get a real, deterministically-sized image to annotate.
test.describe("Image comments", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  const IMAGE = "shelley-inline-image-demo.png";

  // Scratch dirs for the tests that need a PNG of a specific size on disk.
  // Deliberately not removed: the conversation created in one becomes the
  // server's most-recent cwd, and deleting it out from under later tests makes
  // their composer refuse to send ("Invalid working directory"). Same reason
  // editable-file-comments.spec.ts leaves its temp dir behind.
  function scratchPNG(name: string, width: number, height: number): { dir: string; path: string } {
    const dir = mkdtempSync(join(tmpdir(), "shelley-imgcomment-"));
    const path = join(dir, name);
    writeFileSync(path, makePNG(width, height));
    return { dir, path };
  }

  /**
   * A 100x50 JPEG tagged EXIF Orientation=6 (rotate 90° CW for display), so
   * browsers render it 50x100 while a header-only decode reports 100x50.
   * Pre-encoded because that tag is the whole point and no PNG can carry it.
   */
  const ROTATED_JPEG_100x50 =
    "/9j/4AAQSkZJRgABAQAAAQABAAD/4QAiRXhpZgAATU0AKgAAAAgAAQESAAMAAAABAAYAAAAAAAD/2wBDABsSFBcU" +
    "ERsXFhceHBsgKEIrKCUlKFE6PTBCYFVlZF9VXVtqeJmBanGQc1tdhbWGkJ6jq62rZ4C8ybqmx5moq6T/2wBDARwe" +
    "HigjKE4rK06kbl1upKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKT/wAAR" +
    "CAAyAGQDASIAAhEBAxEB/8QAGQAAAwEBAQAAAAAAAAAAAAAAAAIDAQQG/8QAFxABAQEBAAAAAAAAAAAAAAAAAAEC" +
    "Ev/EABoBAQEAAwEBAAAAAAAAAAAAAAMBAAIEBgX/xAAYEQEBAQEBAAAAAAAAAAAAAAAAARECEv/aAAwDAQACEQMR" +
    "AD8A87MGmFZg0w11pO0Zg0wtMNmF0s7SmGzC0waYZpJ2jMGmFZg0wuknaMwaYWmGzC6SdpTBphWYNMLpJ2jwF+Az" +
    "W/txTBphaYbMObXnZ2lMNmFpg0wzSztGYNMKzBphdJO0phswtMNmF0k7SmDTCswaYXSTtGYNMLTDZhmknaPAdHAX" +
    "W/txTBphWYNMOXXnZ2jMGmFZg0wulnaUw2YWmDTC6SdozBphWYNMLpJ2jMGmFphswzSTtKYbMLTBphdJO0eAvwF1" +
    "v7cENAHO+FGw0AUkNGwBSQ0NAGEjYaAKWGhoApI0AK3f/9k=";

  function scratchFile(name: string, contents: Buffer): { dir: string; path: string } {
    const dir = mkdtempSync(join(tmpdir(), "shelley-imgcomment-"));
    const path = join(dir, name);
    writeFileSync(path, contents);
    return { dir, path };
  }

  /** The open comment dialog: where it says it points, and its input. */
  function dialog(page: Page) {
    return {
      where: page.locator(".diff-viewer-comment-dialog-handle"),
      input: page.locator(".diff-viewer-comment-input"),
      add: page.getByRole("button", { name: "Add Comment", exact: true }),
    };
  }

  /** Geometry from the dialog's title, which names the region being commented on. */
  async function dialogGeometry(page: Page): Promise<string> {
    const title = (await dialog(page).where.textContent())!;
    const m = title.match(/\d+x\d+\+\d+\+\d+/);
    expect(m, `dialog title ${title} names a region`).not.toBeNull();
    return m![0];
  }

  /** Drag the middle half of the annotated image (25%..75% in both axes). */
  async function dragMiddleHalf(page: Page, overlay: Locator): Promise<void> {
    // The frame shrink-wraps the image, so its box is the image's displayed
    // pixels and a fraction of it is a fraction of the image.
    const box = await overlay.locator(".image-comment-frame").boundingBox();
    expect(box).not.toBeNull();
    const { x, y, width, height } = box!;
    await page.mouse.move(x + width * 0.25, y + height * 0.25);
    await page.mouse.down();
    await page.mouse.move(x + width * 0.75, y + height * 0.75, { steps: 5 });
    await page.mouse.up();
  }

  /**
   * Assert a geometry is the middle half of a `width`x`height` image.
   *
   * Pointer coordinates are quantized to whole device pixels, so a drag aimed
   * at 25% can land a fraction off and move the result by a pixel. That slack
   * is inherent and harmless: what these tests are about is whether the region
   * is expressed in the right coordinate space at all, and a scaling bug is off
   * by a factor, not by one pixel.
   */
  function expectMiddleHalf(geometry: string, width: number, height: number): void {
    const m = geometry.match(/^(\d+)x(\d+)\+(\d+)\+(\d+)$/);
    expect(m, `geometry ${geometry} is WxH+X+Y`).not.toBeNull();
    const [w, h, x, y] = m!.slice(1).map(Number);
    // Tolerance is in source pixels and scales with how much the image is
    // shrunk on screen, since that is what one device pixel of slop is worth.
    const near = (got: number, want: number, span: number, label: string) => {
      const slack = Math.max(2, Math.ceil(span / 100));
      expect(Math.abs(got - want), `${label}: ${got} vs ${want} (±${slack})`).toBeLessThanOrEqual(
        slack,
      );
    };
    near(w, width / 2, width, "width");
    near(h, height / 2, height, "height");
    near(x, width / 4, width, "x");
    near(y, height / 4, height, "y");
  }

  test("drag a region, comment, and it lands in the message input", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "inline image", { agentTimeout: 60000 });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(`.markdown-content img[src*="${IMAGE}"]`);
    await expect(img).toBeVisible({ timeout: 30000 });
    await img.click();

    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay).toBeVisible();
    await expect(overlay.locator(".image-comment-title")).toHaveText(IMAGE);
    // Drawing is inert until the image reports its intrinsic size, so wait for
    // the hint to flip rather than racing the (re)load of the image bytes.
    await expect(overlay.locator(".image-comment-hint")).toHaveText(/Drag a box/);

    await dragMiddleHalf(page, overlay);

    // Releasing the drag opens the comment dialog for that region, named in the
    // 48x48 image's own pixels.
    const d = dialog(page);
    await expect(d.input).toBeFocused();
    const geometry = await dialogGeometry(page);
    expectMiddleHalf(geometry, 48, 48);

    await d.input.fill("this corner is wrong");
    await d.add.click();

    // The comment goes straight into the message input, and the view stays open
    // so more of the same image can be commented on.
    await expect(d.input).toHaveCount(0);
    await expect(overlay).toBeVisible();
    await expect(overlay.locator(".image-comment-sent")).toContainText("1 comment added");
    // The region stays drawn and numbered, so it is clear what has been covered.
    await expect(overlay.locator(".image-comment-region")).toHaveCount(1);
    await expect(overlay.locator(".image-comment-region-badge")).toHaveText("1");
    await expect(page.getByTestId("message-input")).toHaveValue(
      `> image ${IMAGE} [region ${geometry} of 48x48]\nthis corner is wrong\n\n`,
    );
  });

  test("each comment is inserted as it is written, without a staging step", async ({
    page,
    request,
  }) => {
    const slug = await createConversationViaAPI(request, "inline image", { agentTimeout: 60000 });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(`.markdown-content img[src*="${IMAGE}"]`);
    await expect(img).toBeVisible({ timeout: 30000 });
    await img.click();

    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay).toBeVisible();
    await expect(overlay.locator(".image-comment-hint")).toHaveText(/Drag a box/);
    const box = (await overlay.locator(".image-comment-frame").boundingBox())!;
    const dragRegion = async (x0: number, y0: number, x1: number, y1: number) => {
      await page.mouse.move(box.x + box.width * x0, box.y + box.height * y0);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width * x1, box.y + box.height * y1, { steps: 8 });
      await page.mouse.up();
    };

    const d = dialog(page);
    const messageInput = page.getByTestId("message-input");
    let expected = "";

    // Three regions in a row. Each one's comment appends to the message input as
    // soon as it is written, so the composer is up to date throughout rather
    // than at the end.
    for (const [i, [text, coords]] of (
      [
        ["top left", [0.05, 0.05, 0.3, 0.3]],
        ["middle", [0.4, 0.4, 0.6, 0.6]],
        ["bottom right", [0.7, 0.7, 0.95, 0.95]],
      ] as const
    ).entries()) {
      await dragRegion(...coords);
      const geometry = await dialogGeometry(page);
      await d.input.fill(text);
      await d.add.click();
      expected += `> image ${IMAGE} [region ${geometry} of 48x48]\n${text}\n\n`;
      await expect(messageInput).toHaveValue(expected);
      // Every commented region stays drawn and numbered in order.
      await expect(overlay.locator(".image-comment-region")).toHaveCount(i + 1);
      await expect(overlay.locator(".image-comment-region-badge").nth(i)).toHaveText(`${i + 1}`);
    }

    // Focus must not follow the injected text into the composer behind the
    // still-open view, or the next keystroke would go to the wrong place.
    await expect(messageInput).not.toBeFocused();
    await expect(overlay.locator(".image-comment-container")).toBeFocused();

    // A whole-image comment appends the same way and draws no region.
    await overlay.getByRole("button", { name: "Comment on the whole image" }).click();
    await d.input.fill("and overall");
    await d.add.click();
    expected += `> image ${IMAGE} [whole image, 48x48]\nand overall\n\n`;
    await expect(messageInput).toHaveValue(expected);
    await expect(overlay.locator(".image-comment-region")).toHaveCount(3);
    await expect(overlay.locator(".image-comment-sent")).toContainText("4 comments added");

    // Only closing dismisses the view.
    await overlay.getByRole("button", { name: "Close image comments" }).click();
    await expect(overlay).toHaveCount(0);
    await expect(messageInput).toHaveValue(expected);
  });

  test("comment on the whole image, and Escape discards", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "inline image", { agentTimeout: 60000 });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(`.markdown-content img[src*="${IMAGE}"]`);
    await expect(img).toBeVisible({ timeout: 30000 });

    await img.click();
    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay).toBeVisible();
    const d = dialog(page);

    // Escape backs out of the open comment first, leaving the view up: the text
    // being abandoned is the thing Escape is most likely meant for.
    await overlay.getByRole("button", { name: "Comment on the whole image" }).click();
    await d.input.fill("abandoned");
    await page.keyboard.press("Escape");
    await expect(d.input).toHaveCount(0);
    await expect(overlay).toBeVisible();
    await expect(page.getByTestId("message-input")).toHaveValue("");

    // A second Escape closes the view, still without touching the composer.
    await page.keyboard.press("Escape");
    await expect(overlay).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveValue("");

    // Markdown images are keyboard-reachable: Enter on a focused image opens
    // the view, same as a click.
    await img.focus();
    await page.keyboard.press("Enter");
    await expect(overlay).toBeVisible();

    // A whole-image comment says so in the dialog title, and in the header it
    // writes, instead of naming a region.
    await overlay.getByRole("button", { name: "Comment on the whole image" }).click();
    await expect(d.where).toContainText("whole image");
    await d.input.fill("looks good overall");
    await d.add.click();
    await expect(page.getByTestId("message-input")).toHaveValue(
      `> image ${IMAGE} [whole image, 48x48]\nlooks good overall\n\n`,
    );
  });

  test("screenshot tool images are commentable and reference the file on disk", async ({
    page,
    request,
  }) => {
    test.setTimeout(120000);
    const slug = await createConversationViaAPI(request, "screenshot", { agentTimeout: 90000 });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(".screenshot-tool .commentable-image").first();
    await expect(img).toBeVisible({ timeout: 30000 });
    // The affordance that says the image is clickable at all. This project
    // emulates a Pixel 5, so (hover: none) applies and the badge is always
    // shown; the hover-reveal path is covered separately below.
    await expect(page.locator(".screenshot-tool .commentable-image-badge").first()).toHaveCSS(
      "opacity",
      "1",
    );
    await img.click();

    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay).toBeVisible();
    // The header names the screenshot's on-disk path (Display.path), which the
    // agent can crop, rather than the image endpoint URL.
    await expect(overlay.locator(".image-comment-title")).toHaveText(/\.png$/);
    await overlay.getByRole("button", { name: "Comment on the whole image" }).click();
    await dialog(page).input.fill("check the header");
    await dialog(page).add.click();

    await expect(page.getByTestId("message-input")).toHaveValue(
      /> image \/tmp\/shelley-screenshots\/[^\s]+\.png \[whole image, \d+x\d+\]\ncheck the header/,
    );
  });

  test("regions are reported in the source file's pixels, not the downscaled copy's", async ({
    page,
    request,
  }) => {
    // read_image downscales images that exceed the model's limits, so the
    // rendered image is smaller than the file the comment header names. A
    // region has to be scaled back into the file's coordinates, or the crop
    // the agent runs lands somewhere else entirely.
    // 3001x60, over predictable's 2000px limit and lopsided enough that the
    // downscale's whole-pixel rounding shifts the aspect ratio by 2.5% (it
    // becomes 2000x39). Nothing may key off that drift: only the scale differs,
    // and the region still has to be reported in the source's own space.
    const { dir, path } = scratchPNG("wide.png", 3001, 60);

    const slug = await createConversationViaAPI(request, `read_image: ${path}`, {
      agentTimeout: 60000,
      cwd: dir,
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(".screenshot-tool .commentable-image").first();
    await expect(img).toBeVisible({ timeout: 30000 });
    // The served copy really is downscaled, and did load; otherwise this test
    // proves nothing. (predictable's MaxImageDimension is 2000.)
    await expect
      .poll(() => img.evaluate((el: HTMLImageElement) => el.naturalWidth), { timeout: 15000 })
      .toBeGreaterThan(0);
    expect(await img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeLessThan(3001);
    await img.click();

    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay.locator(".image-comment-hint")).toHaveText(/Drag a box/);

    await dragMiddleHalf(page, overlay);

    // Reported in the 3001x60 source's pixels, not the rendered copy's.
    const geometry = await dialogGeometry(page);
    expectMiddleHalf(geometry, 3001, 60);

    await dialog(page).input.fill("scaled correctly");
    await dialog(page).add.click();
    await expect(page.getByTestId("message-input")).toHaveValue(
      `> image ${path} [region ${geometry} of 3001x60]\nscaled correctly\n\n`,
    );
  });

  // A portrait image is constrained by max-height, not width, which changes the
  // displayed size the drag is measured against; that must not change the
  // geometry that comes out. Desktop is not a formality here: the two viewports
  // take different max-height rules, and a rule that clamped the frame while
  // letting the image overflow it made everything below the fold silently
  // unclickable on desktop only.
  for (const [name, viewport] of [
    ["a phone", { width: 390, height: 844 }],
    ["desktop", { width: 1280, height: 800 }],
  ] as const) {
    test(`a tall image on ${name} viewport still maps regions correctly`, async ({
      page,
      request,
    }) => {
      await page.setViewportSize(viewport);

      const { dir, path } = scratchPNG("tall.png", 400, 1600);

      const slug = await createConversationViaAPI(request, `read_image: ${path}`, {
        agentTimeout: 60000,
        cwd: dir,
      });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      const img = page.locator(".screenshot-tool .commentable-image").first();
      await expect(img).toBeVisible({ timeout: 30000 });
      await img.click();

      const overlay = page.locator(".image-comment-overlay");
      // The hint that gates the other tests is hidden at phone width, so wait
      // on the whole-image button instead: it is disabled until the image
      // reports its size, which is also when drawing becomes live.
      await expect(
        overlay.getByRole("button", { name: "Comment on the whole image" }),
      ).toBeEnabled();

      const frame = overlay.locator(".image-comment-frame");
      // The frame shrink-wraps the image, which is what makes a fraction of the
      // frame a fraction of the image. Assert it rather than assume it.
      const [frameBox, imgBox] = await Promise.all([
        frame.boundingBox(),
        overlay.locator(".image-comment-img").boundingBox(),
      ]);
      expect(frameBox!.x).toBeCloseTo(imgBox!.x, 0);
      expect(frameBox!.y).toBeCloseTo(imgBox!.y, 0);
      expect(frameBox!.width).toBeCloseTo(imgBox!.width, 0);
      expect(frameBox!.height).toBeCloseTo(imgBox!.height, 0);
      // And the whole image is reachable without scrolling, so every part of it
      // can be dragged on.
      expect(imgBox!.height).toBeLessThanOrEqual(viewport.height);

      await dragMiddleHalf(page, overlay);

      expectMiddleHalf(await dialogGeometry(page), 400, 1600);
    });
  }

  test("a touch drag draws a box, and a second finger cannot hijack it", async ({
    page,
    request,
  }) => {
    // Touch is the drag path most likely to break: it is the one with pointer
    // capture, cancellation, and more than one pointer in flight. The default
    // project is a touch device, so drive real touch events via CDP.
    const { dir, path } = scratchPNG("touch.png", 800, 600);
    const slug = await createConversationViaAPI(request, `read_image: ${path}`, {
      agentTimeout: 60000,
      cwd: dir,
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(".screenshot-tool .commentable-image").first();
    await expect(img).toBeVisible({ timeout: 30000 });
    await img.tap();

    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay.getByRole("button", { name: "Comment on the whole image" })).toBeEnabled();

    const box = (await overlay.locator(".image-comment-frame").boundingBox())!;
    const at = (fx: number, fy: number) => ({
      x: box.x + box.width * fx,
      y: box.y + box.height * fy,
    });
    const cdp = await page.context().newCDPSession(page);
    // Detach explicitly: the session outlives the page otherwise.
    // eslint-disable-next-line @typescript-eslint/no-floating-promises
    page.once("close", () => cdp.detach().catch(() => {}));
    const touch = (type: string, points: { x: number; y: number }[]) =>
      cdp.send("Input.dispatchTouchEvent", {
        type,
        touchPoints: points.map((p, i) => ({ ...p, id: i })),
      });

    // Finger one drags the middle half.
    await touch("touchStart", [at(0.25, 0.25)]);
    await touch("touchMove", [at(0.75, 0.75)]);
    const draft = overlay.locator(".image-comment-draft");
    const draftWidth = async () => {
      // Two frames: one for Vue to flush the style, one for layout.
      await page.evaluate(
        () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
      );
      return (await draft.boundingBox())!.width;
    };
    const half = box.width / 2;
    // Percentage layout plus CDP coordinate quantization put this within a
    // pixel; a hijacked drag would be off by a third of the image.
    expect(await draftWidth()).toBeCloseTo(half, 0);

    // Finger two arrives mid-drag and wanders to the far corner. It gets its
    // own pointerId, so the box in flight must not follow it -- if it did, the
    // box would shrink back toward that corner.
    await touch("touchStart", [at(0.75, 0.75), at(0.05, 0.95)]);
    await touch("touchMove", [at(0.75, 0.75), at(0.02, 0.98)]);
    expect(await draftWidth()).toBeCloseTo(half, 0);

    await touch("touchEnd", [at(0.75, 0.75)]);
    await touch("touchEnd", [at(0.02, 0.98)]);

    expectMiddleHalf(await dialogGeometry(page), 800, 600);

    // And the drag really is over: no draft box is left hanging.
    await expect(draft).toHaveCount(0);
  });

  test("a rotated JPEG is commented on in the pixels that were rendered", async ({
    page,
    request,
  }) => {
    // Browsers apply EXIF orientation; a raw pixel read does not. So the tool
    // reports source dimensions already corrected for it (100x50 stored, 50x100
    // as everyone sees it), and the comment carries the instruction to
    // auto-orient -- croppers like `magick` ignore the tag too, and without it
    // the crop would land on a different part of the picture.
    const { dir, path } = scratchFile("rotated.jpg", Buffer.from(ROTATED_JPEG_100x50, "base64"));

    const slug = await createConversationViaAPI(request, `read_image: ${path}`, {
      agentTimeout: 60000,
      cwd: dir,
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(".screenshot-tool .commentable-image").first();
    await expect(img).toBeVisible({ timeout: 30000 });
    // The premise: the browser really does rotate it. Without this the test
    // would pass on a build that never had the bug to fix.
    await expect
      .poll(() => img.evaluate((el: HTMLImageElement) => el.naturalWidth), { timeout: 15000 })
      .toBe(50);
    expect(await img.evaluate((el: HTMLImageElement) => el.naturalHeight)).toBe(100);
    await img.click();

    const overlay = page.locator(".image-comment-overlay");
    await expect(overlay.locator(".image-comment-hint")).toHaveText(/Drag a box/);

    await dragMiddleHalf(page, overlay);

    // The middle half of the image as displayed, in the displayed image's own
    // 50x100 space -- not 100x50, which would describe a different region.
    const geometry = await dialogGeometry(page);
    expectMiddleHalf(geometry, 50, 100);
    await dialog(page).input.fill("rotated");
    await dialog(page).add.click();
    await expect(page.getByTestId("message-input")).toHaveValue(
      `> image ${path} [region ${geometry} of 50x100, auto-orient first]\nrotated\n\n`,
    );
  });

  // The default project emulates a phone, where (hover: none) keeps the badge
  // visible; a mouse-capable context is the only place the reveal is testable.
  test.describe("with a mouse", () => {
    test.use({ viewport: { width: 1280, height: 800 }, hasTouch: false, isMobile: false });

    test("the comment badge appears on hover", async ({ page, request }) => {
      const slug = await createConversationViaAPI(request, "inline image", { agentTimeout: 60000 });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      const img = page.locator(`.markdown-content img[src*="${IMAGE}"]`);
      await expect(img).toBeVisible({ timeout: 30000 });
      const badge = page.locator(".markdown-content .commentable-image-badge").first();
      await expect(badge).toHaveCSS("opacity", "0");
      await img.hover();
      await expect(badge).toHaveCSS("opacity", "1");
    });
  });
});

/** A minimal valid RGB PNG of the given size, filled with a gradient. */
function makePNG(width: number, height: number): Buffer {
  const raw = Buffer.alloc(height * (1 + width * 3));
  for (let y = 0; y < height; y++) {
    const row = y * (1 + width * 3);
    for (let x = 0; x < width; x++) {
      const p = row + 1 + x * 3;
      raw[p] = (x * 7) & 0xff;
      raw[p + 1] = (y * 3) & 0xff;
      raw[p + 2] = (x + y) & 0xff;
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // color type: truecolor
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 1 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function chunk(type: string, data: Buffer): Buffer {
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

const CRC_TABLE = (() => {
  const table = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c;
  }
  return table;
})();

function crc32(buf: Buffer): number {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}
