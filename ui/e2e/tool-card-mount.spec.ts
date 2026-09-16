import { test, expect } from "@playwright/test";

test("defers offscreen completed tool cards and hydrates them near the viewport", async ({
  page,
  request,
}) => {
  const generated = await request.post("/debug/loremipsum?json=1", {
    form: { size: "12", model: "predictable" },
  });
  expect(generated.ok()).toBeTruthy();
  const { conversation_id: conversationId } = await generated.json();

  await page.goto(`/c/${conversationId}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });

  const placeholder = page.locator(".tool-card-mount-placeholder").first();
  await expect(placeholder).toBeAttached({ timeout: 30_000 });
  await expect(placeholder).not.toBeInViewport();
  await expect(placeholder).toHaveAttribute("data-testid", "tool-call-completed");
  await expect(placeholder).toHaveAttribute("role", "group");
  await expect(placeholder).toHaveAttribute("aria-label", / tool result$/);

  const mediaOuterHeight = await page.evaluate(() => {
    const el = document.createElement("div");
    el.className = "tool-card-mount-placeholder tool-card-mount-placeholder--media";
    document.body.append(el);
    const style = getComputedStyle(el);
    const height =
      el.getBoundingClientRect().height +
      Number.parseFloat(style.marginTop) +
      Number.parseFloat(style.marginBottom);
    el.remove();
    return height;
  });
  expect(mediaOuterHeight).toBe(282);

  const toolUseId = await placeholder.getAttribute("data-tool-use-id");
  expect(toolUseId).toBeTruthy();
  const placeholderForTool = page.locator(
    `.tool-card-mount-placeholder[data-tool-use-id="${toolUseId}"]`,
  );
  const fragment = `t-${toolUseId!.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8)}`;

  await page.evaluate((hash) => {
    window.location.hash = hash;
  }, fragment);
  await expect(placeholderForTool).toHaveCount(0, { timeout: 10_000 });

  const hydratedCard = page.locator(
    `.toc-tool-anchor[data-tool-use-id="${toolUseId}"] + [data-testid="tool-call-completed"]`,
  );
  await expect(hydratedCard).toBeVisible({ timeout: 10_000 });
  await expect(hydratedCard).toHaveClass(/message-highlight/);
  await expect(hydratedCard).not.toHaveClass(/message-highlight/, { timeout: 5_000 });

  // Hydration is sticky: scrolling away must not tear down the specialized
  // component and recreate the cheap placeholder.
  await page.locator(".messages-container").evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  await expect(placeholderForTool).toHaveCount(0);
  await expect(hydratedCard).toBeAttached();

  // Printing is eager so a printed transcript cannot contain blank shells.
  await page.evaluate(() => window.dispatchEvent(new Event("beforeprint")));
  await expect(page.locator(".tool-card-mount-placeholder")).toHaveCount(0, {
    timeout: 10_000,
  });
});
