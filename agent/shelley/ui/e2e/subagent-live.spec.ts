import { test, expect } from "@playwright/test";
import { createConversationViaAPI, setPageFeatureFlag } from "./helpers";

// Live subagent visualization: while a subagent conversation is working, the
// parent's subagent tool widget shows a live activity strip (what the
// subagent is doing right now) that opens the subagent when clicked, and the
// drawer's subagent count badge shows running/total.
test.describe("subagent live activity", () => {
  test("card strip shows activity, opens subagent; drawer badge shows running count", async ({
    page,
    request,
  }) => {
    test.setTimeout(120000);
    // Card mode (tool-pills off) — the SubagentTool card renders inline.
    await setPageFeatureFlag(page, "tool-pills", false);

    const slug = await createConversationViaAPI(request, "hello there");
    await page.goto(`/c/${slug}`);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30000 });

    // Kick off a subagent that stays busy for a while.
    await input.fill("subagent: helper bash: sleep 120");
    await page.getByTestId("send-button").click();

    // The live strip appears on the running subagent tool card and shows
    // what the subagent is doing (the bash sleep headline, streamed over
    // the unified stream into the parent's UI).
    const live = page.getByTestId("subagent-live");
    await expect(live).toBeVisible({ timeout: 30000 });
    await expect(live).toContainText("sleep", { timeout: 30000 });

    // Drawer badge: 1 running of 1 total.
    await page.locator('button[aria-label="Open conversations"]').click();
    await expect(page.locator(".drawer.open")).toBeVisible();
    const badge = page.locator(".conversation-item.active .subagent-count-badge");
    await expect(badge).toBeVisible({ timeout: 15000 });
    await expect(badge).toContainText("1/1");
    await expect(badge.locator('[data-testid="subagent-badge-running"]')).toBeVisible();
    await page.locator('button[aria-label="Close conversations"]').click();
    await expect(page.locator(".drawer.open")).toHaveCount(0);

    // Clicking the strip navigates to the subagent conversation.
    await live.click();
    await expect(page).toHaveURL(/\/c\/helper/, { timeout: 10000 });
  });

  test("pill mode shows live segment next to the subagent pill", async ({ page, request }) => {
    test.setTimeout(120000);
    await setPageFeatureFlag(page, "tool-pills", true);

    const slug = await createConversationViaAPI(request, "hello again");
    await page.goto(`/c/${slug}`);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30000 });

    await input.fill("subagent: pill-helper bash: sleep 120");
    await page.getByTestId("send-button").click();

    const live = page.getByTestId("subagent-pill-live");
    await expect(live).toBeVisible({ timeout: 30000 });
    await expect(live).toContainText("sleep", { timeout: 30000 });

    await live.click();
    await expect(page).toHaveURL(/\/c\/pill-helper/, { timeout: 10000 });
  });
});
