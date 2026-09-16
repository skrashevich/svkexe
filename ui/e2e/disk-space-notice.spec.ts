import { test, expect } from "@playwright/test";

const lowStatus = {
  episode_id: 7,
  revision: 3,
  active: true,
  critical: false,
  dismissed: false,
  available_bytes: 1_600_000_000,
};

test.describe("Disk space notice", () => {
  test("shows stream status and dismisses the current episode", async ({ page }) => {
    await page.route("**/api/stream2", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `data: ${JSON.stringify({ disk_space_status: lowStatus })}\n\n`,
      });
    });

    let dismissedEpisode: number | undefined;
    await page.route("**/api/disk-space/dismiss", async (route) => {
      dismissedEpisode = route.request().postDataJSON().episode_id;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...lowStatus, revision: 4, dismissed: true }),
      });
    });

    await page.goto("/");

    const notice = page.getByTestId("disk-space-notice");
    await expect(notice).toBeVisible({ timeout: 30000 });
    await expect(notice).toContainText("Disk space is low");
    await expect(notice).toContainText("1.6 GB remaining");
    await expect(notice).toHaveAttribute("role", "status");

    await page.getByTestId("disk-space-notice-dismiss").click();

    await expect.poll(() => dismissedEpisode).toBe(lowStatus.episode_id);
    await expect(notice).toHaveCount(0);
  });

  test("announces critical status as an alert", async ({ page }) => {
    await page.route("**/api/stream2", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `data: ${JSON.stringify({
          disk_space_status: { ...lowStatus, critical: true, available_bytes: 400_000_000 },
        })}\n\n`,
      });
    });

    await page.goto("/");

    const notice = page.getByTestId("disk-space-notice");
    await expect(notice).toBeVisible({ timeout: 30000 });
    await expect(notice).toContainText("Disk space is critically low");
    await expect(notice).toHaveAttribute("role", "alert");
  });
});
