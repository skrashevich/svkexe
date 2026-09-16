import { test, expect } from "@playwright/test";
import { writeFileSync, mkdirSync } from "node:fs";
import { execSync } from "node:child_process";
import { join } from "node:path";
import { createConversationViaAPI, withTempDir } from "./helpers";

// The fuzzy file finder (Cmd/Ctrl+Shift+P) ANDs whitespace-separated terms, so
// a half-remembered filename typed as words finds the file: "vm storage s3"
// must reach vm-storage-s3-backup-design.md even though the literal query
// (spaces and all) never appears in the path.

test.describe("File finder multi-term search", () => {
  test("space-separated terms are ANDed", async ({ page, request }) => {
    await withTempDir("shelley-finder-", async (dir) => {
      mkdirSync(join(dir, "docs"), { recursive: true });
      for (const name of [
        "vm-storage-s3-backup-design.md",
        "vm-storage-replication.md",
        "s3-uploads.md",
      ]) {
        writeFileSync(join(dir, "docs", name), "x\n");
      }

      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });
      await finderInput.fill("vm storage s3");

      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("docs/vm-storage-s3-backup-design.md");
      // Each term underlines its literal run rather than a scattered subsequence.
      await expect(rows.first().locator("mark")).toHaveText(["vm", "storage", "s3"]);

      // A single term still matches every file containing it.
      await finderInput.fill("vm-storage");
      await expect(rows).toHaveCount(2, { timeout: 10000 });
    });
  });
});

// Typing a path re-roots the finder at that directory, so a file outside the
// conversation's working directory can still be opened in the editor.
test.describe("File finder path queries", () => {
  test("an absolute path searches that directory and opens the file", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-finder-cwd-", async (cwd) => {
      writeFileSync(join(cwd, "local.md"), "local\n");

      await withTempDir("shelley-finder-else-", async (elsewhere) => {
        writeFileSync(join(elsewhere, "handoff-notes.md"), "far away content\n");

        const slug = await createConversationViaAPI(request, "Hello", { cwd });
        await page.goto(`/c/${slug}`);
        await page.waitForLoadState("domcontentloaded");

        await page.keyboard.press("ControlOrMeta+Shift+P");
        const finderInput = page.locator(".grp-filter");
        await expect(finderInput).toBeVisible({ timeout: 10000 });

        await finderInput.fill(join(elsewhere, "handoff"));

        const rows = page.locator(".grp-row");
        await expect(rows).toHaveCount(1, { timeout: 10000 });
        await expect(rows.first()).toContainText("handoff-notes.md");
        // The list is no longer relative to the working directory, so the
        // finder says where it is actually looking.
        await expect(page.locator(".ff-scope")).toContainText(elsewhere);

        // Enter opens the file from the re-rooted directory, not a path
        // joined against the conversation's cwd.
        await finderInput.press("Enter");
        const modal = page.getByRole("dialog", {
          name: `Edit ${join(elsewhere, "handoff-notes.md")}`,
        });
        await expect(modal).toBeVisible({ timeout: 15000 });
        await expect(
          modal.locator(".view-line", { hasText: "far away content" }).first(),
        ).toBeVisible({ timeout: 15000 });
      });
    });
  });

  test("clearing a path query returns to the working directory", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-finder-back-", async (cwd) => {
      writeFileSync(join(cwd, "local-only.md"), "local\n");
      // A sibling to wander into. Not shared /tmp: that's every other test's
      // temp dir, and walking it is both slow and nondeterministic.
      const away = join(cwd, "away");
      mkdirSync(away, { recursive: true });
      writeFileSync(join(away, "remote.md"), "remote\n");

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });

      await finderInput.fill(away + "/");
      await expect(page.locator(".ff-scope")).toContainText(away, { timeout: 10000 });

      await finderInput.fill("local-only");
      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("local-only.md");
      await expect(page.locator(".ff-scope")).toHaveCount(0);
    });
  });

  test("a path to an empty directory says the directory is empty", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-finder-empty-", async (cwd) => {
      writeFileSync(join(cwd, "local.md"), "local\n");
      const empty = join(cwd, "empty-dir");
      mkdirSync(empty, { recursive: true });

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });

      // Naming a directory lists all of it, so an empty one found nothing to
      // list rather than failing to match a pattern.
      await finderInput.fill(empty + "/");
      await expect(page.locator(".grp-empty")).toHaveText("No files in this directory.", {
        timeout: 10000,
      });

      // A pattern that matches nothing inside it reads differently.
      await finderInput.fill(join(empty, "zzz"));
      await expect(page.locator(".grp-empty")).toHaveText("No matching files.", {
        timeout: 10000,
      });
    });
  });
});

// In a git repo the finder also greps file contents, so a term that appears
// nowhere in any file name still finds the file — and the row shows the
// matching line as a snippet with the term highlighted.
test.describe("File finder content search", () => {
  test("a term matching only file content finds the file and shows a snippet", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-finder-grep-", async (dir) => {
      // git grep --untracked searches untracked files, so init suffices.
      execSync("git init", { cwd: dir });
      writeFileSync(join(dir, "recipes.txt"), "secret ingredient: cardamom\n");
      writeFileSync(join(dir, "shopping-list.txt"), "eggs and flour\n");

      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });
      await finderInput.fill("cardamom");

      // "cardamom" matches no file name, so the only row is the content hit.
      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("recipes.txt");

      // The row explains itself: the matching line, with the term marked.
      const snippet = rows.first().locator(".ff-snippet");
      await expect(snippet).toBeVisible();
      await expect(snippet).toContainText("cardamom");
      await expect(snippet.locator("mark")).toHaveText("cardamom");
    });
  });

  test("name matches and content matches share the list, names first", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-finder-grep-mix-", async (dir) => {
      execSync("git init", { cwd: dir });
      // One file matches by name, another only by content: the finder should
      // show both, with the (fast, first-phase) name match on top.
      writeFileSync(join(dir, "cardamom-notes.md"), "about the spice\n");
      writeFileSync(join(dir, "recipes.txt"), "secret ingredient: cardamom\n");

      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });
      await finderInput.fill("cardamom");

      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(2, { timeout: 10000 });
      // Name match first (rendered by the fast pass), content-only hit below.
      await expect(rows.nth(0)).toContainText("cardamom-notes.md");
      await expect(rows.nth(1)).toContainText("recipes.txt");
      await expect(rows.nth(1).locator(".ff-snippet mark")).toHaveText("cardamom");
    });
  });

  test("a stale content response from a superseded keystroke never lands", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-finder-grep-stale-", async (dir) => {
      execSync("git init", { cwd: dir });
      // "cardamom" hits only paprika-notes.txt by content; "paprika" hits it
      // by name. If the first keystroke's (delayed) content response applied
      // after the second keystroke's results, the row would gain a snippet
      // mentioning cardamom — which the second query never asked about.
      writeFileSync(join(dir, "paprika-notes.txt"), "cardamom goes well here\n");
      writeFileSync(join(dir, "other.txt"), "nothing relevant\n");

      // Hold the FIRST content-phase request until released; pass everything
      // else through untouched.
      let releaseFirst: () => void = () => {};
      const firstHeld = new Promise<void>((r) => (releaseFirst = r));
      let held = false;
      await page.route(
        (url) => url.pathname === "/api/find-files" && url.searchParams.get("content") === "only",
        async (route) => {
          if (!held) {
            held = true;
            await firstHeld;
          }
          // The held request has usually been aborted client-side by the
          // superseding keystroke; continue() then throws harmlessly.
          await route.continue().catch(() => {});
        },
      );

      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });

      // First keystroke: content-only hit whose grep response is now stuck.
      await finderInput.fill("cardamom");
      await expect(page.locator(".ff-grep-pending")).toBeVisible({ timeout: 10000 });

      // Second keystroke supersedes it; its own (unheld) phases both land.
      await finderInput.fill("paprika");
      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("paprika-notes.txt");

      // Release the stale response and give it a beat to (not) apply: the
      // row must not sprout the cardamom snippet, and the list must not grow.
      releaseFirst();
      await page.waitForTimeout(300);
      await expect(rows).toHaveCount(1);
      await expect(page.locator(".ff-snippet")).toHaveCount(0);
    });
  });
});
