import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Split out of conversation.spec.ts (see the note there). These cover how tool
// calls are rendered and coalesced in the transcript; they drive the UI from /
// rather than seeding a conversation via the API.
test("coalesces tool calls - shows tool result with details", async ({ page }) => {
  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  const messageInput = page.getByTestId("message-input");
  const sendButton = page.getByTestId("send-button");

  // Send a bash command to trigger tool use
  await messageInput.fill('bash: echo "hello world"');
  await sendButton.click();

  // Wait for the tool result to appear
  await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible({
    timeout: 30000,
  });

  // Verify the bash tool header is visible
  await expect(page.locator(".bash-tool-header").first()).toBeVisible();

  // Verify bash tool shows command
  await expect(page.locator(".bash-tool-command").first()).toBeVisible();
});

test("coalesces tool calls - displays agent text and tool separately", async ({ page }) => {
  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  const messageInput = page.getByTestId("message-input");
  const sendButton = page.getByTestId("send-button");

  // Send a bash command
  await messageInput.fill("bash: pwd");
  await sendButton.click();

  // Wait for tool result
  await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible({
    timeout: 30000,
  });

  // Verify agent message is shown ("I'll run the command: pwd")
  await expect(page.locator("text=I'll run the command: pwd").first()).toBeVisible();

  // Verify tool result is shown separately as coalesced tool call
  await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible();
  await expect(page.locator("text=bash").first()).toBeVisible();
});

test("handles sequential tool calls", async ({ page }) => {
  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  const messageInput = page.getByTestId("message-input");
  const sendButton = page.getByTestId("send-button");

  // First tool call
  await messageInput.fill('bash: echo "first"');
  await sendButton.click();
  await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible({
    timeout: 30000,
  });

  // Second tool call
  await messageInput.fill('bash: echo "second"');
  await sendButton.click();

  // Wait for the second tool result
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 2,
    undefined,
    { timeout: 30000 },
  );

  // Verify both tool calls are displayed
  const toolCalls = page.locator('[data-testid="tool-call-completed"]');
  expect(await toolCalls.count()).toBeGreaterThanOrEqual(2);
});

test("displays LLM error message in UI", async ({ page }) => {
  // Clear any existing data by navigating to root (which should show empty state)
  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  // Wait for the empty state or message input
  const messageInput = page.getByTestId("message-input");
  await expect(messageInput).toBeVisible({ timeout: 30000 });

  const sendButton = page.getByTestId("send-button");

  // Send a message that triggers an error in the predictable LLM
  await messageInput.fill("error: test error message");
  await sendButton.click();

  // Wait for the error message to appear in the UI
  await page.waitForFunction(
    () => {
      const text = "LLM request failed: predictable error: test error message";
      return document.body.textContent?.includes(text) ?? false;
    },
    undefined,
    { timeout: 30000 },
  );

  // Verify error message is visible with error styling
  const errorMessage = page.locator('[role="alert"]');
  await expect(errorMessage).toBeVisible({ timeout: 10000 });

  // Verify the error text is displayed
  await expect(
    page.locator("text=LLM request failed: predictable error: test error message"),
  ).toBeVisible();

  // Verify error label is shown in the message header
  await expect(page.locator('[role="alert"]').locator("text=Error")).toBeVisible();
});
