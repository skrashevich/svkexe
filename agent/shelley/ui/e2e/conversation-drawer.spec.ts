import { expect, test, type Locator, type Page } from "@playwright/test";
import type { ConversationWithState } from "../src/types";

test.use({
  viewport: { width: 1280, height: 720 },
  isMobile: false,
  hasTouch: false,
});

function conversation(
  id: string,
  isDraft = false,
  participants?: Array<string | [string, number]>,
): ConversationWithState {
  return {
    conversation_id: id,
    slug: isDraft ? null : id,
    user_initiated: true,
    created_at: "2026-07-23T12:00:00Z",
    updated_at: "2026-07-23T12:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: "predictable",
    conversation_options: "{}",
    current_generation: 1,
    agent_working: false,
    tags: "[]",
    is_draft: isDraft,
    draft: isDraft ? "unfinished message" : "",
    queued_messages: "[]",
    working: false,
    subagent_count: 0,
    preview: "Preview",
    max_sequence_id: 0,
    participants: participants?.map((participant) => ({
      email: typeof participant === "string" ? participant : participant[0],
      message_count: typeof participant === "string" ? 1 : participant[1],
    })),
  };
}

async function stubConversationList(page: Page, conversations: ConversationWithState[]) {
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations, hash: `test-${conversations.length}` } }),
  );
  await page.route("**/api/conversations/search**", (route) =>
    route.fulfill({ json: conversations }),
  );
  await page.route("**/api/stream2**", (route) => route.abort());
}

function queryEditor(page: Page): Locator {
  return page.getByTestId("conversation-query-editor");
}

async function expectQuery(editor: Locator, raw: string): Promise<void> {
  await expect(editor).toHaveAttribute("data-query-value", raw);
}

function queryToken(editor: Locator, kind: "tag" | "user" | "untagged" | "unattributed") {
  return editor.locator(`[data-conversation-query-token="${kind}"]`);
}

function exactQueryToken(editor: Locator, kind: "tag" | "user", raw: string) {
  return queryToken(editor, kind).filter({ hasText: raw });
}

test.describe("conversation drawer startup and app bar", () => {
  test("single-user lists have no participant filter", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com"]);
    mine.subagent_count = 1;
    const subagent = conversation("subagent", false, ["other@example.com"]);
    subagent.parent_conversation_id = mine.conversation_id;
    await stubConversationList(page, [mine, subagent, conversation("draft", true)]);

    await page.goto("/new");

    await expect(page.locator(".drawer-search-input")).toHaveCount(0);
    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await expect(searchToggle).not.toHaveClass(/search-toggle-active/);
    await expect(page.locator(".conversation-participant-badge")).toHaveCount(0);
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await searchToggle.click();
    await expect(page.getByRole("button", { name: "Add user filter" })).toHaveCount(0);
  });

  test("multi-user lists seed the current-user query once", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [
      conversation("mine", false, [
        ["collaborator@example.com", 2],
        ["me@example.com", 3],
      ]),
      conversation("triple", false, [
        "me@example.com",
        "collaborator@example.com",
        "third@example.com",
      ]),
      conversation("other", false, ["other@example.com"]),
      conversation("draft", true),
    ]);

    await page.goto("/new");

    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await expect(queryEditor(page)).toHaveCount(0);
    await expect(searchToggle).toHaveClass(/search-toggle-active/);
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    // A draft has no messages, hence no participants; it still belongs to
    // whoever is typing it and must survive the seeded current-user filter.
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="other"]')).not.toBeVisible();

    const mineBadge = page.locator('[data-conversation-id="mine"] .conversation-participant-badge');
    await expect(mineBadge).toContainText("2");
    await expect(mineBadge.locator(".conversation-participant-badge-front-filled")).toHaveCount(1);
    await mineBadge.click();
    await expect(page.locator(".p-tooltip")).toContainText("collaborator@example.com");
    await expect(page.locator(".p-tooltip strong")).toHaveText("me@example.com");
    await expect(
      page.locator(".p-tooltip .conversation-participant-tooltip-identity-current"),
    ).toHaveCount(1);
    await expect(page.locator(".p-tooltip .conversation-participant-tooltip-identity")).toHaveCount(
      2,
    );
    const tooltipRows = page.locator(".p-tooltip .conversation-participant-tooltip-row");
    await expect(tooltipRows.nth(0)).toContainText("me@example.com");
    await expect(tooltipRows.nth(1)).toContainText("collaborator@example.com");
    await expect(
      page
        .locator(".p-tooltip .conversation-participant-tooltip-row")
        .filter({ hasText: "collaborator@example.com" })
        .locator("strong"),
    ).toHaveCount(0);
    await expect(
      page
        .locator(".p-tooltip .conversation-participant-tooltip-row")
        .filter({ hasText: "me@example.com" })
        .locator(".conversation-participant-tooltip-count"),
    ).toHaveText("3");

    await searchToggle.click();
    const editor = queryEditor(page);
    await expect(editor).toHaveAttribute("autocapitalize", "none");
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await expectQuery(editor, "user:me@example.com ");
    await expect(exactQueryToken(editor, "user", "user:me@example.com")).toBeVisible();
    await expect(page.getByText("@ Participating", { exact: true })).toHaveCount(0);

    const addUserFilter = page.getByRole("button", { name: "Add user filter" });
    const addTagFilter = page.getByRole("button", { name: "Add tag filter" });
    await addUserFilter.click();
    await expectQuery(editor, "user:me@example.com user:");
    await addTagFilter.click();
    await expectQuery(editor, "user:me@example.com tag:");
    await addUserFilter.click();
    await expectQuery(editor, "user:me@example.com user:");
    await page.keyboard.press("Escape");

    const visibleIds = () =>
      page
        .locator("[data-conversation-id]:visible")
        .evaluateAll((rows) => rows.map((row) => row.getAttribute("data-conversation-id")));
    const defaultOrder = await visibleIds();
    await addUserFilter.click();
    const userPanel = page.getByTestId("user-filter-panel");
    await expect(userPanel.getByRole("option", { name: /other@example\.com/ })).toHaveCount(0);
    await userPanel.getByRole("option", { name: /third@example\.com/ }).click();
    await expectQuery(editor, "user:me@example.com user:third@example.com ");
    await expect(queryToken(editor, "user")).toHaveCount(2);
    await expect(exactQueryToken(editor, "user", "user:third@example.com")).toBeVisible();
    await editor.fill("user:me@example.com ");
    expect(await visibleIds()).toEqual(defaultOrder);

    await searchToggle.click();
    await expect(queryEditor(page)).toHaveCount(0);
    await expect(searchToggle).toHaveClass(/search-toggle-active/);
    await searchToggle.click();
    await expectQuery(editor, "user:me@example.com ");

    await editor.fill("");
    await expect(page.locator('[data-conversation-id="other"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await expect(
      page
        .locator('[data-conversation-id="other"]')
        .getByRole("button", { name: "other@example.com" }),
    ).toContainText("1");
    await expect(queryToken(editor, "user")).toHaveCount(0);
    await addUserFilter.click();
    await expectQuery(editor, "user:");
    await expect(userPanel).toBeVisible();
    await page.locator(".drawer-title").click();
    await expect(userPanel).toHaveCount(0);
    await addUserFilter.click();
    await expect(userPanel).toBeVisible();
    await userPanel.getByRole("option", { name: /me@example\.com/ }).click();
    await expectQuery(editor, "user:me@example.com ");
    await expect(page.locator('[data-conversation-id="other"]')).not.toBeVisible();
    await editor.fill("");
    await searchToggle.click();
    await expect(queryEditor(page)).toHaveCount(0);
    await expect(searchToggle).not.toHaveClass(/search-toggle-active/);
  });

  test("user picker disables exhausted options and reports no matches", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [
      conversation("shared", false, ["me@example.com", "other@example.com"]),
    ]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const addUserFilter = page.getByRole("button", { name: "Add user filter" });
    const userPanel = page.getByTestId("user-filter-panel");
    await addUserFilter.click();
    await page.keyboard.type("missing");
    await expect(userPanel.getByText("No matching users")).toBeVisible();
    await queryEditor(page).fill("user:me@example.com ");
    await addUserFilter.click();
    await userPanel.getByRole("option", { name: /other@example\.com/ }).click();
    await expect(addUserFilter).toBeDisabled();
    await addUserFilter.locator("..").hover();
    await expect(page.locator(".p-tooltip")).toContainText("All users added");
  });

  test("token query editing completes, searches, deletes, and wraps safely", async ({
    page,
    context,
  }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com", "other@example.com"]);
    const other = conversation("other", false, ["other@example.com"]);
    const alpine = conversation("alpine", false, ["third@example.com"]);
    mine.tags = JSON.stringify(["alpha", "in progress", "alpine"]);
    alpine.tags = JSON.stringify(["alpine"]);
    await stubConversationList(page, [mine, other, alpine]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await page.locator(".drawer").evaluate((element: HTMLElement) => {
      element.style.width = "600px";
    });

    const editor = queryEditor(page);
    const tagPanel = page.getByTestId("tag-filter-panel");
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await expect(editor).toHaveAttribute("autocapitalize", "none");
    await expect(editor.locator("p")).toHaveCount(1);
    await expect(exactQueryToken(editor, "user", "user:me@example.com")).toBeVisible();

    // Typing a structured cue opens autocomplete; clicking replaces that
    // exact partial with an atom and one ordinary trailing space.
    await exactQueryToken(editor, "user", "user:me@example.com")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("tag:al");
    await expect(tagPanel).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpha"]')).toBeVisible();
    await tagPanel.locator('[data-tag="alpha"]').click();
    await expectQuery(editor, "user:me@example.com tag:alpha ");
    await expect(exactQueryToken(editor, "tag", "tag:alpha")).toBeVisible();

    // A second typed cue creates a second independent token.
    await page.keyboard.type("tag:alpi");
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await tagPanel.locator('[data-tag="alpine"]').click();
    const committed = "user:me@example.com tag:alpha tag:alpine ";
    await expectQuery(editor, committed);
    await expect(queryToken(editor, "tag")).toHaveCount(2);

    // Free text after atoms remains outside them and drives ordinary search.
    await page.keyboard.type("notes");
    await expectQuery(editor, `${committed}notes`);
    await page.keyboard.press("Escape");
    await expectQuery(editor, committed);

    // Copy addresses the serialized query rather than delete-button chrome.
    await editor.focus();
    await page.keyboard.press("Control+A");
    await page.keyboard.press("Control+C");
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(committed);
    await page.keyboard.press("ArrowRight");

    // The explicit delete button removes one token and its separator.
    await exactQueryToken(editor, "tag", "tag:alpine")
      .getByRole("button", { name: "Remove tag:alpine" })
      .click();
    await expectQuery(editor, "user:me@example.com tag:alpha ");
    await expect(queryToken(editor, "tag")).toHaveCount(1);

    // Re-add it, then prove body clicks place the caret after the separator:
    // typing appends text and Backspace removes only that latest character.
    await exactQueryToken(editor, "tag", "tag:alpha")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("tag:alpi");
    await tagPanel.locator('[data-tag="alpine"]').click();
    await exactQueryToken(editor, "tag", "tag:alpine")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("Z");
    await expectQuery(editor, `${committed}Z`);
    await page.keyboard.press("Backspace");
    await expectQuery(editor, committed);

    // Plain structured-looking text is not an atom, so Backspace removes only
    // its trailing separator rather than deleting the whole visible term.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.keyboard.type("tag:missing ");
    await page.keyboard.press("Backspace");
    await expectQuery(editor, "tag:missing");
    await expect(queryToken(editor, "tag")).toHaveCount(0);

    // Pasted line breaks become query separators without creating a second,
    // unserialized ProseMirror paragraph.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.evaluate(() => navigator.clipboard.writeText("alpha\nbeta"));
    await page.keyboard.press("Control+V");
    await expectQuery(editor, "alpha beta");
    await expect(editor.locator("p")).toHaveCount(1);

    // Extra spaces are deleted normally before Backspace reaches the atom.
    await page.locator(".drawer-search-clear").click();
    await page.getByRole("button", { name: "Add user filter" }).click();
    await page
      .getByTestId("user-filter-panel")
      .getByRole("option", { name: /me@example\.com/ })
      .click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="alpha"]').click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="alpine"]').click();
    await expectQuery(editor, committed);
    await page.keyboard.press("Space");
    await expectQuery(editor, `${committed} `);
    await page.keyboard.press("Backspace");
    await expectQuery(editor, committed);

    // Removing the first of adjacent atoms preserves an insertion slot before
    // the second; text typed there remains separate and deletes normally.
    await exactQueryToken(editor, "tag", "tag:alpha")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.press("Backspace");
    const middleSlot = "user:me@example.com  tag:alpine ";
    await expectQuery(editor, middleSlot);
    await expect(exactQueryToken(editor, "tag", "tag:alpha")).toHaveCount(0);
    await page.keyboard.type("Z");
    await expectQuery(editor, "user:me@example.com Z tag:alpine ");
    await page.keyboard.press("Backspace");
    await expectQuery(editor, middleSlot);

    // Space completion also creates an atom and leaves typing outside it.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.keyboard.type("tag:alpi");
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await page.keyboard.press("Space");
    await expectQuery(editor, "tag:alpine ");
    await expect(exactQueryToken(editor, "tag", "tag:alpine")).toBeVisible();
    await page.keyboard.type("followup");
    await expectQuery(editor, "tag:alpine followup");

    // A space inside an open quoted tag stays literal and keeps autocomplete.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.keyboard.type('tag:"in');
    await expect(tagPanel.locator('[data-tag="in progress"]')).toBeVisible();
    await page.keyboard.press("Space");
    await expectQuery(editor, 'tag:"in ');
    await expect(tagPanel).toBeVisible();

    // Atom nodes wrap only as whole units, never within a token.
    await page.locator(".drawer-search-clear").click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="in progress"]').click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="alpha"]').click();
    await page.locator(".drawer").evaluate((element: HTMLElement) => {
      element.style.width = "250px";
    });
    const tokenLayout = await queryToken(editor, "tag").evaluateAll((tokens) =>
      tokens.map((token) => {
        const prefix = token.querySelector<HTMLElement>(".conversation-query-token-prefix");
        const value = token.querySelector<HTMLElement>(".conversation-query-token-value");
        if (!prefix || !value) throw new Error("token segments missing");
        const tokenRect = token.getBoundingClientRect();
        return {
          height: tokenRect.height,
          prefixTop: prefix.getBoundingClientRect().top,
          valueTop: value.getBoundingClientRect().top,
          whiteSpace: getComputedStyle(token).whiteSpace,
        };
      }),
    );
    expect(tokenLayout).toHaveLength(2);
    for (const token of tokenLayout) {
      expect(token.height).toBeLessThan(30);
      expect(token.prefixTop).toBe(token.valueTop);
      expect(token.whiteSpace).toBe("nowrap");
    }
  });

  test("user and tag filters never hide subagent chats", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const parent = conversation("parent", false, ["me@example.com", "other@example.com"]);
    parent.tags = JSON.stringify(["keep"]);
    parent.subagent_count = 1;
    const subagent = conversation("subagent", false, ["third@example.com"]);
    subagent.parent_conversation_id = parent.conversation_id;
    subagent.tags = JSON.stringify(["different"]);
    await stubConversationList(page, [parent, subagent]);

    await page.goto("/new");
    await expect(page.locator('[data-conversation-id="parent"]')).toBeVisible();
    await page.getByRole("button", { name: "Expand subagents" }).click();
    const subagentRow = page.locator(".subagent-item").filter({ hasText: "subagent" });
    await expect(subagentRow).toBeVisible();

    await page.getByRole("button", { name: "Search conversations..." }).click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await page.getByTestId("tag-filter-panel").locator('[data-tag="keep"]').click();
    await expectQuery(queryEditor(page), "user:me@example.com tag:keep ");
    await expect(subagentRow).toBeVisible();
  });

  test("Backspace on a pill ahead of a partial term keeps query and list in sync", async ({
    page,
  }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com", "other@example.com"]);
    mine.tags = JSON.stringify(["alpha"]);
    const other = conversation("other", false, ["other@example.com"]);
    const alpine = conversation("alpine", false, ["third@example.com"]);
    alpine.tags = JSON.stringify(["alpine"]);
    await stubConversationList(page, [mine, other, alpine]);
    // Vue reports a throwing computed via console.error; the aborted stream
    // stub's resource failure is the one expected error.
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error" && !message.text().startsWith("Failed to load resource")) {
        errors.push(message.text());
      }
    });

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    const tagPanel = page.getByTestId("tag-filter-panel");
    const count = page.locator(".drawer-filter-result-count");
    await expectQuery(editor, "user:me@example.com ");
    await expect(count).toHaveText("1");

    // A trailing partial tag, then the caret moved back to sit right after
    // the user pill's separator (a pill body click lands it there).
    const userPill = exactQueryToken(editor, "user", "user:me@example.com").locator(
      ".conversation-query-token-value",
    );
    await userPill.click();
    await page.keyboard.type("tag:al");
    await expectQuery(editor, "user:me@example.com tag:al");
    await expect(tagPanel.locator('[data-tag="alpha"]')).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpine"]')).toHaveCount(0);
    await expect(count).toHaveText("1");
    await userPill.click();

    // Removing the pill shifts the partial left. The active edit must follow
    // the new offsets: the participant scope is gone, so the dropdown now
    // offers tags from the whole list, and once it closes every row is back.
    await page.keyboard.press("Backspace");
    await expectQuery(editor, " tag:al");
    await expect(queryToken(editor, "user")).toHaveCount(0);
    await expect(tagPanel.locator('[data-tag="alpha"]')).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(tagPanel).toHaveCount(0);
    await expectQuery(editor, " tag:al");
    await expect(count).toHaveText("3");
    await expect(page.locator('[data-conversation-id="other"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="alpine"]')).toBeVisible();
    expect(errors).toEqual([]);
  });

  test("an exactly typed tag or user outranks more popular superstrings", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const exact = conversation("exact", false, ["me@example.com", "other@example.com"]);
    exact.tags = JSON.stringify(["alpha"]);
    const more = ["more-1", "more-2", "more-3"].map((id) => {
      const row = conversation(id, false, ["me@example.com", "other@example.com.au"]);
      row.tags = JSON.stringify(["alpha-more"]);
      return row;
    });
    await stubConversationList(page, [exact, ...more]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await expectQuery(editor, "user:me@example.com ");

    // `alpha-more` has three conversations to `alpha`'s one, but the typed
    // value is exactly `alpha`, so Space must commit that and not the more
    // popular superstring.
    const tagPanel = page.getByTestId("tag-filter-panel");
    await exactQueryToken(editor, "user", "user:me@example.com")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("tag:alph");
    await expect(tagPanel.getByRole("option").first()).toHaveAttribute("data-tag", "alpha-more");
    await page.keyboard.type("a");
    await expect(tagPanel.getByRole("option").first()).toHaveAttribute("data-tag", "alpha");
    await expect(tagPanel.locator('[aria-selected="true"]')).toHaveAttribute("data-tag", "alpha");
    await page.keyboard.press("Space");
    await expectQuery(editor, "user:me@example.com tag:alpha ");
    await expect(exactQueryToken(editor, "tag", "tag:alpha")).toBeVisible();

    // Same rule for emails, committed with Enter: `other@example.com.au` is
    // on three conversations, `other@example.com` on one.
    await editor.fill("user:me@example.com ");
    await page.getByRole("button", { name: "Add user filter" }).click();
    await expectQuery(editor, "user:me@example.com user:");
    const userPanel = page.getByTestId("user-filter-panel");
    await expect(userPanel.getByRole("option").first()).toHaveText(/^other@example\.com\.au\s*3$/);
    await page.keyboard.type("other@example.com");
    await expect(userPanel.getByRole("option")).toHaveCount(2);
    await expect(userPanel.getByRole("option").first()).toHaveText(/^other@example\.com\s*1$/);
    await page.keyboard.press("Enter");
    await expectQuery(editor, "user:me@example.com user:other@example.com ");
  });

  test("multiplayer capability follows the active list, not search results", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [conversation("mine", false, ["me@example.com"])]);
    // A search hit from an archived multi-user conversation must not flip the
    // participant UI on mid-session.
    const foreign = conversation("foreign", false, ["me@example.com", "other@example.com"]);
    foreign.archived = true;
    await page.route("**/api/conversations/search**", (route) =>
      route.fulfill({ json: [foreign] }),
    );

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await expectQuery(editor, "");
    await expect(page.getByRole("button", { name: "Add user filter" })).toHaveCount(0);

    await editor.fill("foreign");
    await expect(page.locator('[data-conversation-id="foreign"]')).toBeVisible();
    await expect(page.getByRole("button", { name: "Add user filter" })).toHaveCount(0);
    await expect(page.locator(".conversation-participant-badge")).toHaveCount(0);
    await editor.fill("");
    await expectQuery(editor, "");
  });

  test("participant UI appears without a current user when a chat has several", async ({
    page,
  }) => {
    await stubConversationList(page, [
      conversation("shared", false, ["alice@example.com", "bob@example.com"]),
      conversation("solo", false, ["alice@example.com"]),
    ]);

    await page.goto("/new");
    await expect(
      page.locator('[data-conversation-id="shared"] .conversation-participant-badge'),
    ).toContainText("2");
    // No current email means nothing to seed the query with.
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await expectQuery(queryEditor(page), "");
    await expect(page.getByRole("button", { name: "Add user filter" })).toBeEnabled();
    await page.getByRole("button", { name: "Group conversations" }).click();
    await expect(page.getByRole("button", { name: "Participants", exact: true })).toBeVisible();
  });

  test("group by participants is offered only for multi-participant lists", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    // Two participants exist across the list, but no conversation has more
    // than one: there is nothing to group by.
    await stubConversationList(page, [
      conversation("mine", false, ["me@example.com"]),
      conversation("other", false, ["other@example.com"]),
    ]);

    await page.goto("/new");
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    await page.getByRole("button", { name: "Group conversations" }).click();
    await expect(page.getByRole("button", { name: "Tags", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Participants", exact: true })).toHaveCount(0);
  });

  test("group by participants buckets whole participant sets, current user first", async ({
    page,
  }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "adam@poyo.co" });
    await stubConversationList(page, [
      conversation("zed-pair", false, ["zed@example.com", "yan@example.com"]),
      conversation("mine", false, ["adam@poyo.co"]),
      // Same set, opposite order and casing: these must land in ONE group.
      conversation("shared", false, ["aaaa@bbb.com", "adam@poyo.co"]),
      conversation("shared-again", false, ["ADAM@POYO.CO", "aaaa@bbb.com"]),
      conversation("alice-pair", false, ["alice@example.com", "bob@example.com"]),
      conversation("none", false, []),
    ]);

    await page.goto("/new");
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    // Drop the seeded current-user filter so every group is on screen.
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await queryEditor(page).fill("");
    await expect(page.locator('[data-conversation-id="none"]')).toBeVisible();

    await page.getByRole("button", { name: "Group conversations" }).click();
    await page.getByRole("button", { name: "Participants", exact: true }).click();

    await expect(page.locator(".conversation-group-label")).toHaveText([
      "adam@poyo.co",
      "aaaa@bbb.com, adam@poyo.co",
      "alice@example.com, bob@example.com",
      "yan@example.com, zed@example.com",
      "Unattributed",
    ]);
    const sharedGroup = page.locator(".conversation-group").filter({
      has: page.locator('[data-conversation-id="shared"]'),
    });
    await expect(sharedGroup).toHaveCount(1);
    await expect(sharedGroup.locator('[data-conversation-id="shared-again"]')).toHaveCount(1);
    await expect(sharedGroup.locator(".conversation-group-label")).toHaveAttribute(
      "title",
      "aaaa@bbb.com, adam@poyo.co",
    );
    await expect(page.locator('[data-conversation-id="shared"]')).toHaveCount(1);
  });

  test("starts expanded even when the only item is a draft", async ({ page }) => {
    await stubConversationList(page, [conversation("draft", true)]);

    await page.goto("/new");

    await expect(page.locator(".drawer")).not.toHaveClass(/collapsed/);
    await expect(page.getByRole("button", { name: "Collapse sidebar" })).toBeVisible();
  });

  test("starts expanded for multiple conversations with aligned app-bar titles", async ({
    page,
  }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await expect(page.locator(".drawer-title")).toBeVisible();

    const metrics = await page.evaluate(() => {
      function inspect(selector: string) {
        const element = document.querySelector<HTMLElement>(selector);
        if (!element) throw new Error(`Missing ${selector}`);
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return {
          top: rect.top,
          bottom: rect.bottom,
          fontFamily: style.fontFamily,
          fontSize: style.fontSize,
          fontWeight: style.fontWeight,
          lineHeight: style.lineHeight,
          letterSpacing: style.letterSpacing,
          margin: style.margin,
        };
      }
      return {
        drawerHeader: inspect(".drawer-header"),
        chatHeader: inspect(".header"),
        drawerTitle: inspect(".drawer-title"),
        chatTitle: inspect(".header-title"),
      };
    });

    expect(metrics.drawerHeader.top).toBe(metrics.chatHeader.top);
    expect(metrics.drawerHeader.bottom).toBe(metrics.chatHeader.bottom);
    expect(metrics.drawerTitle.top).toBe(metrics.chatTitle.top);
    expect(metrics.drawerTitle.bottom).toBe(metrics.chatTitle.bottom);
    expect(metrics.drawerTitle.fontFamily).toBe(metrics.chatTitle.fontFamily);
    expect(metrics.drawerTitle.fontSize).toBe(metrics.chatTitle.fontSize);
    expect(metrics.drawerTitle.fontWeight).toBe(metrics.chatTitle.fontWeight);
    expect(metrics.drawerTitle.lineHeight).toBe(metrics.chatTitle.lineHeight);
    expect(metrics.drawerTitle.letterSpacing).toBe(metrics.chatTitle.letterSpacing);
    expect(metrics.drawerTitle.margin).toBe("0px");
    expect(metrics.chatTitle.margin).toBe("0px");
  });

  test("manual collapse survives reload with a single conversation", async ({ page }) => {
    await stubConversationList(page, [conversation("only")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(drawer).toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Expand sidebar" }).click();
    await expect(drawer).not.toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).not.toHaveClass(/collapsed/);
  });

  test("manual collapse survives reload with many conversations", async ({ page }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(drawer).toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).toHaveClass(/collapsed/);
  });
});

async function swipe(
  page: Page,
  selector: string,
  start: { x: number; y: number },
  end: { x: number; y: number },
) {
  await page.evaluate(
    ({ selector, start, end }) => {
      const target = document.querySelector(selector);
      if (!(target instanceof Element)) throw new Error(`Missing ${selector}`);

      const makeTouch = ({ x, y }: { x: number; y: number }) =>
        new Touch({
          identifier: 1,
          target,
          clientX: x,
          clientY: y,
          screenX: x,
          screenY: y,
          pageX: x,
          pageY: y,
        });
      const startTouch = makeTouch(start);
      const endTouch = makeTouch(end);

      target.dispatchEvent(
        new TouchEvent("touchstart", {
          bubbles: true,
          cancelable: true,
          touches: [startTouch],
          targetTouches: [startTouch],
          changedTouches: [startTouch],
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchmove", {
          bubbles: true,
          cancelable: true,
          touches: [endTouch],
          targetTouches: [endTouch],
          changedTouches: [endTouch],
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchend", {
          bubbles: true,
          cancelable: true,
          touches: [],
          targetTouches: [],
          changedTouches: [endTouch],
        }),
      );
    },
    { selector, start, end },
  );
}

async function swipePatchDiff(page: Page) {
  await page.evaluate(() => {
    const mainContent = document.querySelector(".main-content");
    if (!(mainContent instanceof HTMLElement)) throw new Error("Missing main content");

    const diffsContainer = document.createElement("diffs-container");
    const shadowRoot = diffsContainer.shadowRoot ?? diffsContainer.attachShadow({ mode: "open" });
    const scrollContainer = document.createElement("div");
    const diffLine = document.createElement("span");
    diffsContainer.style.cssText = "display:block;width:120px";
    scrollContainer.style.cssText = "overflow-x:auto;width:120px";
    diffLine.style.cssText = "display:block;width:320px";
    scrollContainer.append(diffLine);
    shadowRoot.append(scrollContainer);
    mainContent.append(diffsContainer);
    Object.defineProperties(scrollContainer, {
      clientWidth: { value: 120 },
      scrollWidth: { value: 320 },
    });

    if (scrollContainer.scrollWidth <= scrollContainer.clientWidth) {
      throw new Error("Patch diff test fixture is not horizontally scrollable");
    }

    const makeTouch = (x: number, y: number) =>
      new Touch({
        identifier: 1,
        target: diffLine,
        clientX: x,
        clientY: y,
        screenX: x,
        screenY: y,
        pageX: x,
        pageY: y,
      });
    const startTouch = makeTouch(160, 300);
    const endTouch = makeTouch(220, 304);

    diffLine.dispatchEvent(
      new TouchEvent("touchstart", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [startTouch],
        targetTouches: [startTouch],
        changedTouches: [startTouch],
      }),
    );
    diffLine.dispatchEvent(
      new TouchEvent("touchmove", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [endTouch],
        targetTouches: [endTouch],
        changedTouches: [endTouch],
      }),
    );
    diffLine.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [],
        targetTouches: [],
        changedTouches: [endTouch],
      }),
    );
  });
}

test.describe("mobile drawer swipe", () => {
  test.use({
    viewport: { width: 393, height: 851 },
    isMobile: true,
    hasTouch: true,
  });

  test("opens and closes with opposite page swipes", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    const drawer = page.locator(".drawer");
    const input = page.getByTestId("message-input");
    await expect(drawer).not.toHaveClass(/open/);
    await input.focus();
    await expect(input).toBeFocused();

    await swipe(page, ".main-content", { x: 180, y: 300 }, { x: 256, y: 304 });
    await expect(drawer).toHaveClass(/open/);
    await expect(input).not.toBeFocused();

    await swipe(page, ".backdrop", { x: 380, y: 300 }, { x: 328, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);
  });

  test("keeps filter text lowercase-ready", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    await swipe(page, ".main-content", { x: 180, y: 300 }, { x: 256, y: 304 });
    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await searchToggle.click();
    await expect(page.locator(".p-tooltip")).toHaveCount(0);
    await page.getByRole("button", { name: "Add tag filter" }).click();

    const filterText = page.locator(".drawer-search-input");
    await expectQuery(filterText, "tag:");
    await expect(filterText).toHaveAttribute("autocapitalize", "none");
    await expect(filterText).toContainText("tag:");
    await expect(queryToken(filterText, "tag")).toHaveCount(0);
  });

  test("leaves the system edge, short movement, and vertical scrolling alone", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await swipe(page, ".main-content", { x: 12, y: 300 }, { x: 112, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);

    await swipe(page, ".main-content", { x: 160, y: 300 }, { x: 224, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);

    await swipe(page, ".main-content", { x: 64, y: 200 }, { x: 92, y: 300 });
    await expect(drawer).not.toHaveClass(/open/);
  });

  test("leaves horizontal patch diff scrolling alone", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    await swipePatchDiff(page);

    await expect(page.locator(".drawer")).not.toHaveClass(/open/);
  });

  test("does not open while a diff viewer modal is shown", async ({ page }) => {
    const first = conversation("first");
    first.cwd = "/tmp";
    await stubConversationList(page, [first]);
    await page.goto("/new");

    await page.keyboard.press("Control+Shift+D");
    await expect(page.locator(".diff-viewer-overlay")).toBeVisible();

    await swipe(page, ".diff-viewer-header", { x: 180, y: 100 }, { x: 256, y: 104 });

    await expect(page.locator(".drawer")).not.toHaveClass(/open/);
  });
});
