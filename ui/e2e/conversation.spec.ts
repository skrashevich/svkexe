import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// The tool-call rendering tests were split out into
// conversation-tool-calls.spec.ts: at 47s of test time this file was one of the
// specs gating the playwright shards, and files are the sharding unit (see
// .buildkite/steps/shelley-playwright-shard.py), so one file cannot be spread
// across lanes.
test.describe("Shelley Conversation Tests", () => {
  test("can send Hello and get greeting response", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    // Wait for the message input using improved selector
    const messageInput = page.getByTestId("message-input");
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Send "Hello" and expect specific predictable response
    await messageInput.fill("Hello");

    // Find and click the send button using improved selector
    const sendButton = page.getByTestId("send-button");
    await expect(sendButton).toBeVisible();
    await sendButton.click();

    // Wait for the response from the predictable model
    // The predictable model responds to "Hello" with "Hello! I'm Shelley, your AI assistant. How can I help you today?"
    await page.waitForFunction(
      () => {
        const text = "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 },
    );

    // Verify both the user message and assistant response are visible
    await expect(page.locator("text=Hello").first()).toBeVisible();
    await expect(
      page.locator("text=Hello! I'm Shelley, your AI assistant. How can I help you today?").first(),
    ).toBeVisible();
  });

  test("can use echo command", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send "echo: test message" and expect echo response
    await messageInput.fill("echo: test message");
    await sendButton.click();

    // The predictable model should echo back "test message"
    await page.waitForFunction(
      () => document.body.textContent?.includes("test message") ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Verify both input and output messages are visible
    await expect(page.locator("text=echo: test message")).toBeVisible();
  });

  test("responds differently to lowercase hello", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send "hello" (lowercase) and expect different response
    await messageInput.fill("hello");
    await sendButton.click();

    // The predictable model responds to "hello" with "Well, hi there!"
    await page.waitForFunction(
      () => document.body.textContent?.includes("Well, hi there!") ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Verify the hello message and response are both visible
    await expect(page.getByText("Well, hi there!").first()).toBeVisible();
  });

  test("shows thinking indicator while awaiting response", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Use a deliberate delay so the indicator exists long enough to observe.
    await messageInput.fill("delay: 2");
    await sendButton.click();

    const thinkingIndicator = page.getByTestId("agent-thinking");
    await expect(thinkingIndicator).toBeVisible({ timeout: 5000 });

    await page.waitForFunction(
      () => document.body.textContent?.includes("Delayed for 2 seconds") ?? false,
      undefined,
      { timeout: 30000 },
    );

    await expect(thinkingIndicator).toBeHidden({ timeout: 10000 });
  });

  test("shows thinking indicator on follow-up messages", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    await messageInput.fill("hello");
    await sendButton.click();

    await page.waitForFunction(
      () => document.body.textContent?.includes("Well, hi there!") ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Use delay command so the thinking indicator is visible long enough to test
    await messageInput.fill("delay: 2");
    await sendButton.click();

    const thinkingIndicator = page.getByTestId("agent-thinking");
    await expect(thinkingIndicator).toBeVisible({ timeout: 5000 });

    await page.waitForFunction(
      () => document.body.textContent?.includes("Delayed for 2 seconds") ?? false,
      undefined,
      { timeout: 30000 },
    );

    await expect(thinkingIndicator).toBeHidden({ timeout: 10000 });
  });

  test("can use bash tool", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send a message that triggers tool use
    await messageInput.fill('bash: echo "hello world"');
    await sendButton.click();

    // The predictable model should use the bash tool and show the response
    await page.waitForFunction(
      () => {
        const text = 'I\'ll run the command: echo "hello world"';
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 },
    );

    // Verify tool usage appears in the UI with coalesced tool call
    await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible({
      timeout: 10000,
    });
    // Check that the tool name "bash" is visible
    await expect(page.locator("text=bash").first()).toBeVisible();
  });

  test("gives default response for undefined messages", async ({ page, request }) => {
    // Create the conversation via API so we avoid a race between the initial
    // SSE Subscribe and the agent message being published.
    const slug = await createConversationViaAPI(request, "this is an undefined message");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // Verify the undefined message and default response are visible
    await expect(
      page.locator("text=edit predictable.go to add a response for that one...").first(),
    ).toBeVisible({ timeout: 30000 });
    await expect(page.locator("text=this is an undefined message").first()).toBeVisible();
  });

  test("conversation persists and displays correctly", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send first message
    await messageInput.fill("Hello");
    await sendButton.click();

    // Wait for first response
    await page.waitForFunction(
      () => {
        const text = "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 },
    );

    // Send second message
    await messageInput.fill("echo: second message");
    await sendButton.click();

    // Wait for second response
    await page.waitForFunction(
      () => document.body.textContent?.includes("second message") ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Verify both responses are still visible (conversation persists)
    await expect(
      page.locator("text=Hello! I'm Shelley, your AI assistant. How can I help you today?").first(),
    ).toBeVisible();
    await expect(page.locator("text=second message").first()).toBeVisible();
  });

  test("can send message with Enter key", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Type message and press Enter
    await messageInput.fill("Hello");
    await messageInput.press("Enter");

    // Verify response
    await page.waitForFunction(
      () => {
        const text = "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 },
    );

    // Verify the Hello message and response are visible
    await expect(
      page.locator("text=Hello! I'm Shelley, your AI assistant. How can I help you today?").first(),
    ).toBeVisible();
  });

  test("handles think tool correctly", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send a message that triggers think tool
    await messageInput.fill("think: I need to analyze this problem");
    await sendButton.click();

    // The predictable model should return thinking content and text response
    await page.waitForFunction(
      () => document.body.textContent?.includes("I've considered my approach.") ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Verify thinking content appears in the UI (rendered as .thinking-content with 💭 emoji, not a tool call)
    await expect(page.locator('[data-testid="thinking-content"]').first()).toBeVisible({
      timeout: 10000,
    });
    await expect(page.locator("text=💭").first()).toBeVisible();
  });

  test("handles patch tool correctly", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send a message that triggers patch tool
    await messageInput.fill("patch: test.txt");
    await sendButton.click();

    // The predictable model should use the patch tool
    await page.waitForFunction(
      () => document.body.textContent?.includes("I'll patch the file: test.txt") ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Verify patch tool usage appears in the UI
    await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible({
      timeout: 10000,
    });
    await expect(page.locator("text=patch").first()).toBeVisible();
  });

  test("displays tool results with collapsible details", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    // Send a bash command that will show tool results
    await messageInput.fill('bash: echo "testing tool results"');
    await sendButton.click();

    // Wait for the tool call to appear
    await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible({
      timeout: 30000,
    });

    // Check for bash tool header (collapsible element)
    const bashToolHeader = page.locator(".bash-tool-header");
    await expect(bashToolHeader.first()).toBeVisible({ timeout: 10000 });
  });

});
