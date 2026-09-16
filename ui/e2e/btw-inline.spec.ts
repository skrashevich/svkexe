import { test, expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConversationViaAPIWithDetails } from "./helpers";

function barrier(name: string): { command: string; cleanup: () => void } {
  const dir = mkdtempSync(join(tmpdir(), `shelley-btw-${name}-`));
  const fifo = join(dir, "release");
  execFileSync("mkfifo", [fifo]);
  return {
    command: `bash: read release < ${fifo}`,
    cleanup: () => rmSync(dir, { recursive: true, force: true }),
  };
}

async function openParent(page: Page, request: APIRequestContext): Promise<Locator> {
  const { slug } = await createConversationViaAPIWithDetails(request, "echo: parent ready");
  await page.goto(`/c/${slug}`);
  const input = page.getByTestId("message-input");
  await expect(input).toBeVisible({ timeout: 30_000 });
  return input;
}

async function send(page: Page, value: string): Promise<void> {
  const input = page.getByTestId("message-input");
  await input.fill(value);
  await page.getByTestId("send-button").click();
  await expect(input).toHaveValue("", { timeout: 30_000 });
}

async function expectReaderCompleted(
  inline: Locator,
  request: APIRequestContext,
  completedTurns: number,
): Promise<void> {
  const childID = await inline.getAttribute("data-btw-exchange-id");
  expect(childID).toBeTruthy();
  await expect
    .poll(async () => {
      const response = await request.get(`/api/conversation/${childID}`);
      if (!response.ok()) return 0;
      const body = (await response.json()) as {
        messages?: Array<{ type: string; end_of_turn?: boolean }>;
      };
      return (
        body.messages?.filter((message) => message.type === "agent" && message.end_of_turn === true)
          .length ?? 0
      );
    })
    .toBeGreaterThanOrEqual(completedTurns);
  await expect(inline.getByRole("button", { name: "Summarize to input" })).toBeVisible({
    timeout: 30_000,
  });
  await expect(inline.locator("[data-btw-follow-up]")).toBeEnabled();
}

test.describe("inline BTW reader", () => {
  test.describe.configure({ mode: "serial" });

  test("keeps parent and sibling readers independent through follow-up, summary, and reload", async ({
    page,
    request,
  }) => {
    const parentBarrier = barrier("parent");
    try {
      const input = await openParent(page, request);
      await send(page, parentBarrier.command);
      const stop = page.getByRole("button", { name: "Stop", exact: true });
      await expect(stop).toBeVisible();

      await send(page, "/btw echo: side one");
      await send(page, "/btw echo: side two");
      await expect(page.getByTestId("btw-inline")).toHaveCount(2, { timeout: 30_000 });
      const first = page.getByTestId("btw-inline").filter({ hasText: "echo: side one" });
      const second = page.getByTestId("btw-inline").filter({ hasText: "echo: side two" });
      await expect(first.locator(".btw-inline-answer")).toContainText("side one");
      await expect(second.locator(".btw-inline-answer")).toContainText("side two");
      await expectReaderCompleted(first, request, 1);
      await expectReaderCompleted(second, request, 1);

      await stop.click();
      await expect(stop).toHaveCount(0, { timeout: 30_000 });
      await expect(page.getByTestId("btw-inline")).toHaveCount(2);

      const followUp = first.locator("[data-btw-follow-up]");
      await followUp.fill("unsent follow-up");
      await first.locator(".btw-inline-label").click();
      await expect(first.locator(".btw-inline-body")).toBeHidden();
      await first.locator(".btw-inline-label").click();
      await expect(followUp).toHaveValue("unsent follow-up");
      await followUp.fill("echo: follow-up answer");
      await first.getByRole("button", { name: "Submit follow up" }).click();
      await expect(first.locator(".btw-inline-answer").last()).toContainText("follow-up answer", {
        timeout: 30_000,
      });
      await expectReaderCompleted(first, request, 2);

      await first.getByRole("button", { name: "Summarize to input" }).click();
      const summary = "edit predictable.go to add a response for that one...";
      await expect(input).toHaveValue(summary, { timeout: 30_000 });

      await page.reload();
      await expect(input).toHaveValue(summary, { timeout: 30_000 });
      await expect(page.getByTestId("btw-inline")).toHaveCount(2);
      await first.getByRole("link", { name: "Open subagent" }).click();
      await expect(page).toHaveURL(/\/c\/btw-/, { timeout: 10_000 });
    } finally {
      parentBarrier.cleanup();
    }
  });

  test("isolates child cancellation and restores a post-clear reader after reload", async ({
    page,
    request,
  }) => {
    const childBarrier = barrier("child");
    try {
      await openParent(page, request);
      await send(page, `/btw ${childBarrier.command}`);
      const inline = page.getByTestId("btw-inline").filter({ hasText: "read release" });
      await inline.getByRole("button", { name: "Cancel" }).click();
      await expect(inline).toContainText("Cancelled", { timeout: 30_000 });
      await expect(page.getByRole("button", { name: "Stop", exact: true })).toHaveCount(0);

      await send(page, "echo: parent still works");
      await expect(page.getByText("parent still works").last()).toBeVisible({ timeout: 30_000 });
      await send(page, "/clear");
      await expect(page.locator(".generation-divider")).toHaveCount(1, { timeout: 30_000 });
      await send(page, "/btw echo: after clear");
      const afterClear = page.getByTestId("btw-inline").filter({ hasText: "echo: after clear" });
      await expect(afterClear.locator(".btw-inline-answer")).toContainText("after clear", {
        timeout: 30_000,
      });

      await page.reload();
      await expect(afterClear.locator(".btw-inline-answer")).toContainText("after clear", {
        timeout: 30_000,
      });
    } finally {
      childBarrier.cleanup();
    }
  });
});
