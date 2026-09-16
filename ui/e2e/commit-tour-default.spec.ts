import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPI, withTempDir } from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

test.describe("Commit tour defaults", () => {
  test("a clean HEAD tour opens by default with tour contents in the sidebar", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-tour-viewer-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(repo);
      git(repo, "init");
      git(repo, "config", "user.name", "Tour Test");
      git(repo, "config", "user.email", "tour@example.com");

      const lines = Array.from({ length: 220 }, (_, index) => `line ${index + 1}`);
      writeFileSync(join(repo, "example.txt"), lines.join("\n") + "\n");
      git(repo, "add", "example.txt");
      git(repo, "commit", "-m", "Base commit\n\nPrompt: tour viewer base");
      git(repo, "branch", "upstream");
      git(repo, "switch", "-c", "feature");
      git(repo, "branch", "--set-upstream-to=upstream", "feature");

      lines[1] = "updated near the top";
      lines[198] = "updated near the bottom";
      writeFileSync(join(repo, "example.txt"), lines.join("\n") + "\n");
      git(repo, "add", "example.txt");
      git(repo, "commit", "-m", "Tour the top commit\n\nPrompt: tour viewer feature");

      const scaffold = JSON.parse(
        execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], {
          encoding: "utf8",
        }),
      );
      expect(scaffold.chunks).toHaveLength(2);
      const tour = {
        ...scaffold,
        title: "Tour the top commit",
        intro: Array.from(
          { length: 24 },
          (_, index) => `Overview paragraph ${index + 1} explains the guided change.`,
        ).join("\n\n"),
        chunks: [
          { header: "## Core behavior" },
          ...scaffold.chunks.map((chunk: { ref: number }, index: number) => ({
            ...chunk,
            comment: `Guided change ${index + 1}.`,
          })),
        ],
      };
      const tourPath = join(tempDir, "tour.json");
      writeFileSync(tourPath, JSON.stringify(tour));
      execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
      await page.goto(`/c/${slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
      await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
      await page.locator(".overflow-menu-item", { hasText: /diffs/i }).click();

      const overlay = page.locator(".diff-viewer-overlay");
      await expect(overlay).toBeVisible({ timeout: 30000 });
      await expect(overlay.getByRole("radio", { name: "Single commit" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour");
      await expect(overlay.locator(".commit-tour-introduction h1")).toHaveText(
        "Tour the top commit",
      );

      const workingRow = overlay.locator(".diff-viewer-commit-list-item.working");
      await expect(workingRow.locator(".working-changes-status")).toHaveText("Clean");
      await expect(workingRow).toHaveAttribute("title", "No working changes");

      await expect(overlay.locator(".diff-viewer-toast-hint")).toHaveCount(0);
      await expect(overlay.getByText("Table of Contents", { exact: true })).toBeVisible();
      const contents = overlay.getByRole("navigation", { name: "Tour contents" });
      await expect(contents.getByRole("button", { name: "Overview" })).toBeVisible();
      const section = contents.getByRole("button", { name: "Core behavior" });
      await expect(section).toBeVisible();
      await expect(contents.getByRole("button", { name: /example\.txt/ })).toHaveCount(2);
      await expect(overlay.locator(".diff-tree")).toHaveCount(0);

      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Files" }).click();
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Files");
      await expect(overlay.locator(".diff-tree")).toBeVisible();
      await expect(overlay.getByRole("navigation", { name: "Tour contents" })).toHaveCount(0);
      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Tour" }).click();
      await expect(overlay.getByRole("navigation", { name: "Tour contents" })).toBeVisible();
      await expect(overlay.locator(".diff-viewer-toast-hint")).toHaveCount(0);

      await section.click();
      const tourView = overlay.locator(".commit-tour-view");
      await expect
        .poll(() => tourView.evaluate((element) => element.scrollTop))
        .toBeGreaterThan(100);
      await expect
        .poll(() =>
          overlay.locator("#tour-entry-0").evaluate((target) => {
            const view = target.closest(".commit-tour-view");
            if (!view) return Number.POSITIVE_INFINITY;
            return Math.abs(target.getBoundingClientRect().top - view.getBoundingClientRect().top);
          }),
        )
        .toBeLessThan(40);
    });
  });
});
