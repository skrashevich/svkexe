import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// All tests create the conversation via the API and then navigate directly to
// it. This avoids the SSE subscribe-vs-publish race that occurs when the
// browser opens a brand-new conversation while the first turn is still being
// recorded (see helpers.ts), which otherwise flakes waitForSelector(".message-agent").
// Split out of the original markdown.spec.ts, which at 47s of test time was
// one of the specs gating the playwright shards (see
// .buildkite/steps/shelley-playwright-shard.py -- files are the sharding unit,
// so a single file cannot be spread across lanes). The two halves are
// independent: every test creates its own conversation via the API.
test.describe("Markdown sanitization", () => {
  test("strips script tags from agent messages", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: hello <script>alert("xss")</script> world',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    // The text should be there, but no script element
    await expect(agent).toContainText("hello", { timeout: 30000 });
    await expect(agent).toContainText("world");
    expect(await agent.locator("script").count()).toBe(0);
    // Also confirm the alert text doesn't appear anywhere in the raw HTML
    const html = await agent.innerHTML();
    expect(html).not.toContain("<script");
    expect(html).not.toContain("alert");
  });

  test("strips remote img tags (image tracking)", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      "markdown: ![tracker](https://evil.com/pixel.gif) safe text",
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe text", { timeout: 30000 });
    expect(await agent.locator("img").count()).toBe(0);
    const html = await agent.innerHTML();
    expect(html).not.toContain("<img");
    expect(html).not.toContain("evil.com");
  });

  test("strips iframe tags", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <iframe src="https://evil.com"></iframe> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    expect(await agent.locator("iframe").count()).toBe(0);
  });

  test("strips event handler attributes", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <div onclick="alert(1)">click me</div>',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("click me", { timeout: 30000 });
    const html = await agent.innerHTML();
    expect(html).not.toContain("onclick");
    expect(html).not.toContain("alert");
  });

  test("sanitizes javascript: href in links", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <a href="javascript:alert(document.cookie)">steal cookies</a>',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("steal cookies", { timeout: 30000 });
    const html = await agent.innerHTML();
    expect(html).not.toContain("javascript:");
  });

  test("strips SVG with embedded script", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <svg onload="alert(1)"><circle r="50"/></svg> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    const html = await agent.innerHTML();
    expect(html).not.toContain("<svg");
    expect(html).not.toContain("onload");
  });

  test("strips non-checkbox input elements (phishing prevention)", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <input type="text" placeholder="Enter password"> <input type="password"> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    // Text and password inputs should be stripped
    expect(await agent.locator('input[type="text"]').count()).toBe(0);
    expect(await agent.locator('input[type="password"]').count()).toBe(0);
  });

  test("strips form and input[type=submit] (phishing prevention)", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <form action="https://evil.com/steal"><button type="submit">Login</button></form> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    // Inspect just the rendered markdown content; the surrounding action bar
    // legitimately contains <button> (copy/usage) and must be excluded.
    const content = agent.locator('[data-testid="message-content"]');
    const html = await content.innerHTML();
    expect(html).not.toContain("<form");
    expect(html).not.toContain("<button");
    expect(html).not.toContain("evil.com");
  });
});
