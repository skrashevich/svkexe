import { test, expect } from "@playwright/test";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConversationViaAPI } from "./helpers";

// The standalone "Edit file" modal (fuzzy finder -> EditableFileModal) offers
// the same edit/comment mode toggle as the diff viewer. Comment mode makes
// the editor read-only and click-to-comment: clicking a line opens the Add
// Comment dialog, and the submitted comment lands in the chat message input
// as a quoted block ("> path:line: code\ncomment").

test.describe("Edit-file modal comment mode", () => {
  // The mode toggle and click-to-comment flow are exercised on desktop (the
  // default project viewport is mobile-sized).
  test.use({ viewport: { width: 1280, height: 800 } });

  test("comment on a line flows into the message input", async ({ page, request }) => {
    test.setTimeout(60000);

    // A file with known content, in a cwd the conversation (and the file
    // finder, which searches the conversation cwd) points at.
    const dir = mkdtempSync(join(tmpdir(), "shelley-editfile-"));
    const filePath = join(dir, "notes.txt");
    writeFileSync(filePath, "alpha first line\nbravo second line\ncharlie third line\n");

    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // Open the fuzzy file finder and pick the file.
    await page.keyboard.press("ControlOrMeta+Shift+P");
    const finderInput = page.locator(".grp-filter");
    await expect(finderInput).toBeVisible({ timeout: 10000 });
    await finderInput.fill("notes.txt");
    await expect(page.locator(".grp-body").getByText("notes.txt")).toBeVisible({
      timeout: 10000,
    });
    await finderInput.press("Enter");

    // The editor modal opens with Monaco and the mode toggle.
    const modal = page.getByRole("dialog", { name: /Edit .*notes\.txt/ });
    await expect(modal).toBeVisible({ timeout: 15000 });
    await expect(modal.locator(".monaco-editor").first()).toBeVisible({ timeout: 15000 });

    // Edit mode is the default; switch to comment mode.
    const commentModeBtn = modal.getByRole("button", { name: "Comment mode" });
    const editModeBtn = modal.getByRole("button", { name: "Edit mode" });
    await expect(editModeBtn).toHaveClass(/active/);
    await commentModeBtn.click();
    await expect(commentModeBtn).toHaveClass(/active/);

    // Click a line to open the Add Comment dialog for it.
    await modal.locator(".view-line", { hasText: "bravo second line" }).click();
    const dialog = page.locator(".diff-viewer-comment-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("Add Comment (Line 2)");
    await expect(dialog.locator(".diff-viewer-selected-text")).toContainText("bravo second line");

    // Clicking the same line again retargets the dialog: the text follows, and
    // focus comes back to it. The label is identical in that case, so nothing
    // derived from the label can be what notices.
    await dialog.locator(".diff-viewer-comment-input").fill("needs more bravado");
    await dialog.locator(".diff-viewer-comment-input").blur();
    await modal.locator(".view-line", { hasText: "bravo second line" }).click();
    await expect(dialog).toContainText("Add Comment (Line 2)");
    await expect(dialog.locator(".diff-viewer-comment-input")).toHaveValue("needs more bravado");
    await expect(dialog.locator(".diff-viewer-comment-input")).toBeFocused();

    await dialog.getByRole("button", { name: "Add Comment" }).click();
    await expect(dialog).not.toBeVisible();

    // Close the modal; the quoted comment is injected into the message input.
    await page.keyboard.press("Escape");
    await expect(modal).not.toBeVisible();
    const input = page.getByTestId("message-input");
    await expect(input).toHaveValue(new RegExp(`> ${filePath}:2: bravo second line`));
    await expect(input).toHaveValue(/needs more bravado/);
  });
});
