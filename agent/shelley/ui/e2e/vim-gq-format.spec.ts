import { test, expect, type Page } from "@playwright/test";
import { writeFileSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPI, withTempDir } from "./helpers";

// monaco-vim (CodeMirror's vim keymap) never implemented the gq/gw format
// operators, so `gqip` in a Shelley editor silently did nothing. We register
// our own operator in services/monaco.ts. These tests drive real keystrokes
// through the vim adapter in the file editor and check the reflowed text
// lands both on screen and in the auto-saved file.

// Open `file` (which lives in the conversation's cwd) in the editor modal via
// the file finder, enable vim, and focus the editor ready for normal-mode
// keystrokes. Returns the modal, its Monaco root and the vim status bar.
async function openFileWithVim(page: Page, file: string, basename: string, visibleText: string) {
  await page.keyboard.press("ControlOrMeta+Shift+P");
  const finderInput = page.locator(".grp-filter");
  await expect(finderInput).toBeVisible({ timeout: 10000 });
  await finderInput.fill(basename);
  await expect(page.locator(".grp-row")).toHaveCount(1, { timeout: 10000 });
  await finderInput.press("Enter");

  const modal = page.getByRole("dialog", { name: `Edit ${file}` });
  await expect(modal).toBeVisible({ timeout: 15000 });
  const monaco = modal.locator(".monaco-editor").first();
  await expect(monaco.locator(".view-line", { hasText: visibleText })).toBeVisible({
    timeout: 15000,
  });

  // Enable Vim (desktop-only toggle) and wait for the status bar to
  // confirm normal mode.
  const vimToggle = modal.locator(".vim-toggle");
  await expect(vimToggle).toBeVisible();
  await vimToggle.click();
  const vimStatus = modal.locator(".monaco-vim-status");
  await expect(vimStatus).toContainText("NORMAL", { timeout: 10000 });

  await monaco.locator("textarea.inputarea").focus();
  return { modal, monaco, vimStatus };
}

test.describe("Vim gq paragraph formatting", () => {
  // The Vim toggle is desktop-only (rendered when innerWidth >= 768).
  test.use({ viewport: { width: 1280, height: 800 } });

  test("gqip reflows the paragraph under the cursor to textwidth", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-vim-gq-", async (cwd) => {
      const file = join(cwd, "notes.md");
      // Paragraph 1 is a long ragged line plus a short one; paragraph 2 must
      // be left untouched by `ip`.
      const long =
        "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa";
      writeFileSync(file, `${long}\nquebec romeo\n\nsecond paragraph stays\n`);

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      const { modal, monaco, vimStatus } = await openFileWithVim(
        page,
        file,
        "notes",
        "quebec romeo",
      );

      // Go to the top, then format the paragraph.
      await page.keyboard.type("gg");
      await page.keyboard.type("gqip");

      // Paragraph 1 is now wrapped at 79 columns: the 97-char line splits
      // and "quebec romeo" joins onto the tail.
      const viewLines = monaco.locator(".view-lines");
      await expect(viewLines).toContainText("papa quebec romeo", { timeout: 10000 });
      await expect(viewLines).toContainText("second paragraph stays");
      await expect(vimStatus).toContainText("NORMAL");

      // The editor auto-saves; verify the on-disk result is a proper reflow.
      await expect(async () => {
        const lines = readFileSync(file, "utf8").split("\n");
        expect(lines.slice(0, 4)).toEqual([
          "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike",
          "november oscar papa quebec romeo",
          "",
          "second paragraph stays",
        ]);
      }).toPass({ timeout: 10000 });

      // Undo restores the original text in one step.
      await page.keyboard.type("u");
      await expect(viewLines).toContainText(long, { timeout: 10000 });
      await expect(async () => {
        expect(readFileSync(file, "utf8")).toBe(
          `${long}\nquebec romeo\n\nsecond paragraph stays\n`,
        );
      }).toPass({ timeout: 10000 });

      await modal.locator(".diff-viewer-close").click();
      await expect(modal).toBeHidden({ timeout: 10000 });
    });
  });

  test("gqip in a Go file re-applies the // comment leader to wrapped lines", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-vim-gq-go-", async (cwd) => {
      const file = join(cwd, "doc.go");
      // A ragged doc comment above a declaration. The comment paragraph is
      // three lines; the `func` line after it must not be pulled in.
      const comment = [
        "// Frobnicate applies the frobnication transform to every element of xs",
        "// in",
        "// place and reports how many elements changed as a result.",
      ];
      const original = `package doc\n\n${comment.join("\n")}\nfunc Frobnicate(xs []int) int { return 0 }\n`;
      writeFileSync(file, original);

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      const { modal, monaco, vimStatus } = await openFileWithVim(page, file, "doc.go", "// in");

      // Line 3 is the first comment line; `ip` covers the comment block and
      // the func line (no blank between them), but the leader change keeps
      // the func line in its own chunk.
      await page.keyboard.type("3G");
      await page.keyboard.type("gqip");

      const viewLines = monaco.locator(".view-lines");
      await expect(viewLines).toContainText("// place and reports", { timeout: 10000 });
      await expect(vimStatus).toContainText("NORMAL");

      await expect(async () => {
        expect(readFileSync(file, "utf8")).toBe(
          [
            "package doc",
            "",
            "// Frobnicate applies the frobnication transform to every element of xs in",
            "// place and reports how many elements changed as a result.",
            "func Frobnicate(xs []int) int { return 0 }",
            "",
          ].join("\n"),
        );
      }).toPass({ timeout: 10000 });

      await modal.locator(".diff-viewer-close").click();
      await expect(modal).toBeHidden({ timeout: 10000 });
    });
  });
});
