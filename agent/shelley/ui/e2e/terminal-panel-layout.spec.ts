import { test, expect } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test("keeps the terminal inside its padded content area", async ({ page, request }) => {
  await page.addInitScript(() => {
    const sizeMessages: Array<{ type: string; cols: number; rows: number }> = [];
    Object.defineProperty(window, "__terminalSizeMessages", { value: sizeMessages });
    const send = WebSocket.prototype.send;
    WebSocket.prototype.send = function (data) {
      if (typeof data === "string") {
        try {
          const message = JSON.parse(data);
          if (message.type === "init" || message.type === "resize") {
            sizeMessages.push(message);
          }
        } catch {
          // Non-JSON terminal input is irrelevant to this layout test.
        }
      }
      Reflect.apply(send, this, [data]);
    };
  });

  const terminalSizeMessages = () =>
    page.evaluate(
      () =>
        (
          window as typeof window & {
            __terminalSizeMessages: Array<{ type: string; cols: number; rows: number }>;
          }
        ).__terminalSizeMessages,
    );

  const { conversationId } = await createConversationViaAPIWithDetails(
    request,
    "terminal panel layout test",
  );
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto(`/c/${conversationId}`);

  await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
  await page.locator(".overflow-menu-item", { hasText: /terminal/i }).click();

  const terminal = page.locator(
    '.terminal-panel-content [data-terminal-id][style*="display: block"]',
  );
  await expect(terminal).toBeVisible({ timeout: 30000 });

  const expectTerminalInsideContent = async () => {
    await expect
      .poll(async () =>
        terminal.evaluate((el) => {
          const terminalRect = el.getBoundingClientRect();
          const contentRect = el.parentElement!.getBoundingClientRect();
          const screenRect = el.querySelector(".xterm-screen")!.getBoundingClientRect();
          return {
            terminalBottom: terminalRect.bottom <= contentRect.bottom,
            terminalRight: terminalRect.right <= contentRect.right,
            screenBottom: screenRect.bottom <= terminalRect.bottom,
            screenRight: screenRect.right <= terminalRect.right,
          };
        }),
      )
      .toEqual({
        terminalBottom: true,
        terminalRight: true,
        screenBottom: true,
        screenRight: true,
      });
  };

  await expectTerminalInsideContent();
  await expect
    .poll(
      async () =>
        (await terminalSizeMessages()).find((message) => message.type === "init")?.cols ?? 0,
    )
    .toBeGreaterThan(0);
  const initialCols = (await terminalSizeMessages()).find(
    (message) => message.type === "init",
  )!.cols;

  await page.getByLabel("Minimize terminals").click();
  await expect(terminal).not.toBeVisible();
  await page.setViewportSize({ width: 900, height: 720 });
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(resolve));
      }),
  );
  await page.evaluate(() => {
    (
      window as typeof window & {
        __terminalSizeMessages: Array<{ type: string; cols: number; rows: number }>;
      }
    ).__terminalSizeMessages.length = 0;
  });
  await page.getByLabel("Expand terminals").click();
  await expect(terminal).toBeVisible();

  await expectTerminalInsideContent();
  await expect
    .poll(
      async () =>
        (await terminalSizeMessages()).findLast((message) => message.type === "resize")?.cols ??
        initialCols,
    )
    .toBeLessThan(initialCols);
});
