import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Regression tests for typing performance in large conversations (Safari).
//
// Root cause of the original bug, in two layers:
//
// 1. ChatInterface tracked the composer text in a reactive ref, so every
//    keystroke re-rendered the whole ChatInterface component tree — including
//    the `updated` hook of every mounted PrimeVue directive (v-tooltip).
//
// 2. PrimeVue's BaseDirective._loadStyles ends with _themeChangeListener,
//    which clears the "loaded style names" registry. The next directive
//    update therefore re-ran loadCSS and rewrote the (identical) CSS text
//    into the shared <style data-primevue-style-id> elements. Replacing a
//    stylesheet's contents — even with identical text — forces WebKit to
//    invalidate and recalculate style for the entire document: ~660ms per
//    keystroke in a 5000-message conversation.
//
// Fixed by (a) making the composer text non-reactive in ChatInterface
// (MessageInput owns it; the parent is only notified via emits and seeds the
// composer imperatively), and (b) patching @primevue/core's useStyle to skip
// no-op textContent writes (see ui/patches/).
test.describe("Typing performance", () => {
  test("typing does not rewrite stylesheets or re-render the conversation", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo message 0");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const input = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");
    await expect(input).toBeVisible({ timeout: 30000 });

    // Add messages so the conversation is scrollable.
    for (let i = 1; i < 4; i++) {
      await input.fill(`echo message ${i}`);
      await sendButton.click();
      await expect(page.locator(`text=echo message ${i}`).last()).toBeVisible({ timeout: 30000 });
      await expect(page.getByTestId("agent-thinking")).toBeHidden({ timeout: 30000 });
    }

    // Scroll up so the v-tooltip'd scroll-to-bottom button mounts. Before the
    // fix, each keystroke re-rendered ChatInterface, re-running that
    // directive's `updated` hook, which rewrote PrimeVue's shared <style>
    // elements — a whole-document style invalidation in WebKit — per
    // keystroke.
    const messagesContainer = page.locator(".messages-container");
    await messagesContainer.evaluate((el) => {
      el.scrollTop = 0;
    });
    await expect(page.locator(".scroll-to-bottom-button")).toBeVisible({ timeout: 10000 });

    await page.evaluate(() => {
      const state = window as Window & {
        __styleWrites?: number;
        __messagesListPatches?: number;
      };
      state.__styleWrites = 0;
      state.__messagesListPatches = 0;

      // Count <style> element rewrites (each one forces WebKit to restyle the
      // whole document).
      const desc = Object.getOwnPropertyDescriptor(Node.prototype, "textContent");
      if (!desc?.set || !desc?.get) throw new Error("Node.textContent accessors not found");
      Object.defineProperty(HTMLStyleElement.prototype, "textContent", {
        configurable: true,
        get() {
          return desc.get!.call(this);
        },
        set(v) {
          state.__styleWrites = (state.__styleWrites || 0) + 1;
          desc.set!.call(this, v);
        },
      });

      // Count DOM mutations under the messages list; a keystroke must not
      // touch the conversation DOM at all.
      const list = document.querySelector(".messages-list");
      if (!list) throw new Error(".messages-list not found");
      new MutationObserver((records) => {
        state.__messagesListPatches = (state.__messagesListPatches || 0) + records.length;
      }).observe(list, { subtree: true, childList: true, attributes: true, characterData: true });
    });

    await input.click();
    await input.pressSequentially("hello performance world");

    const counts = await page.evaluate(() => {
      const state = window as Window & {
        __styleWrites?: number;
        __messagesListPatches?: number;
      };
      return {
        styleWrites: state.__styleWrites || 0,
        messagesListPatches: state.__messagesListPatches || 0,
      };
    });
    expect(counts.styleWrites).toBe(0);
    expect(counts.messagesListPatches).toBe(0);
  });
});
