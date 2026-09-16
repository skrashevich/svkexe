import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// ConfigFieldInput.vue renders the per-channel-type config fields inside the
// Notifications "Add channel" form. It was migrated from bare <select>/<input>
// to PrimeVue <Select>/<InputText>. See components/ConfigFieldInput.vue.
//
// The ntfy channel type is the richest consumer: it has text fields, password
// fields, and two <Select>s (Done/Error Priority). We exercise the PrimeVue
// controls here and confirm the migrated value flow still drives the form.
test.describe("Config field input (PrimeVue)", () => {
  test("ntfy add-channel form uses InputText + Select and updates the model", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // Open Notification Settings via the command palette.
    await page.keyboard.press("ControlOrMeta+k");
    const search = page.locator(".command-palette-input");
    await expect(search).toBeVisible();
    await search.fill("notification");
    const item = page.locator(".command-palette-item").first();
    await expect(item).toBeVisible();
    await item.click();

    // Go to the add-channel form and pick the ntfy channel type.
    await page.getByRole("button", { name: "+ Add Channel" }).click();
    await page.locator(".provider-btn", { hasText: /^ntfy$/ }).click();

    // The "Topic" text field is a PrimeVue InputText (config-<name> id kept so
    // the <label for> still resolves). Typing must flow back through the
    // migrated @update:model-value -> change emit path.
    const topic = page.locator("#config-topic");
    await expect(topic).toHaveClass(/p-inputtext/);
    await topic.fill("my-topic");
    await expect(topic).toHaveValue("my-topic");

    // The "Password" field is an InputText of type=password, and its
    // aria-describedby must resolve to the description text (kept from the
    // bare-<input> contract).
    const pw = page.locator("#config-password");
    await expect(pw).toHaveAttribute("type", "password");
    const pwDesc = await pw.getAttribute("aria-describedby");
    expect(pwDesc).toBe("config-password-desc");
    await expect(page.locator("#config-password-desc")).toBeVisible();

    // "Done Priority" is a PrimeVue Select seeded with the server default.
    const donePriority = page
      .locator(".form-group", { hasText: "Done Priority" })
      .locator(".p-select");
    await expect(donePriority).toBeVisible();
    await expect(donePriority.locator(".p-select-label")).toHaveText("default");

    // Open the overlay and pick "max"; the trigger label must update, proving
    // the option list and the change emit both work.
    await donePriority.click();
    const overlay = page.locator(".p-select-overlay");
    await expect(overlay).toBeVisible();
    await expect(overlay.locator(".p-select-option")).toHaveText([
      "min",
      "low",
      "default",
      "high",
      "max",
    ]);
    await overlay.locator(".p-select-option", { hasText: /^max$/ }).click();
    await expect(donePriority.locator(".p-select-label")).toHaveText("max");
  });
});
