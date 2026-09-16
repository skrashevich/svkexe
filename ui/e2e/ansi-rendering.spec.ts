import { test, expect } from '@playwright/test';
import { createConversationViaAPI, openToolPill, setPageFeatureFlag } from './helpers';

test.describe('ANSI escape sequence rendering', () => {
  // Pill rendering is gated behind a feature flag; opt in for this suite.
  test.beforeEach(async ({ page }) => {
    await setPageFeatureFlag(page, 'tool-pills', true);
  });

  test('bash output with ANSI colors renders styled text, not raw escapes', async ({ page, request }) => {
    // Run a command that produces ANSI-colored output
    const slug = await createConversationViaAPI(request, `bash: printf '\\033[32mGreen\\033[0m \\033[31mRed\\033[0m \\033[1mBold\\033[0m \\033[33mYellow\\033[0m plain'`);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Bash tool calls render as compact pills; opening the pill
    // surfaces the full BashTool view in a modal.
    const modal = await openToolPill(page, 'printf');
    const bashTool = modal.locator('.bash-tool');
    await expect(bashTool).toBeVisible({ timeout: 15000 });
    const details = bashTool.locator('.bash-tool-details');
    await expect(details).toBeVisible();

    // The output section is the last .bash-tool-code that is NOT .bash-tool-code-cwd
    // and NOT the command section (which doesn't have error class).
    // Find the output <pre> — it's rendered by AnsiText and should have <span> children
    // when ANSI codes are present.
    const outputPre = details.locator('.bash-tool-code').last();
    await expect(outputPre).toBeVisible();

    // The output should NOT contain raw escape characters like [0m or \033
    const textContent = await outputPre.textContent();
    expect(textContent).not.toContain('[0m');
    expect(textContent).not.toContain('[32m');
    expect(textContent).not.toContain('[31m');
    expect(textContent).not.toContain('\x1b');

    // The output SHOULD contain the readable text
    expect(textContent).toContain('Green');
    expect(textContent).toContain('Red');
    expect(textContent).toContain('Bold');
    expect(textContent).toContain('Yellow');
    expect(textContent).toContain('plain');

    // The output should use dangerouslySetInnerHTML with <span> tags for colors
    const innerHTML = await outputPre.innerHTML();
    expect(innerHTML).toContain('<span');
    expect(innerHTML).toContain('style=');
    expect(innerHTML).toContain('color');

    // Verify specific colors are applied via inline styles
    // Green = color:rgb(0,187,0) (ansi_up default for color 2)
    // Red = color:rgb(187,0,0) (ansi_up default for color 1)
    const greenSpan = outputPre.locator('span').filter({ hasText: 'Green' });
    await expect(greenSpan).toBeVisible();
    const greenStyle = await greenSpan.getAttribute('style');
    expect(greenStyle).toContain('color');

    const redSpan = outputPre.locator('span').filter({ hasText: 'Red' });
    await expect(redSpan).toBeVisible();
    const redStyle = await redSpan.getAttribute('style');
    expect(redStyle).toContain('color');

    // Bold is rendered as a span with font-weight:bold (ansi_up style)
    const boldSpan = outputPre.locator('span').filter({ hasText: 'Bold' });
    await expect(boldSpan).toBeVisible();
    const boldStyle = await boldSpan.getAttribute('style');
    expect(boldStyle).toContain('font-weight:bold');

    // Take a screenshot for visual verification
    await page.screenshot({ path: 'e2e/screenshots/ansi-colors.png', fullPage: true });
  });

  test('bash output without ANSI codes renders as plain text', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'bash: echo "just plain text with no escapes"');

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Scope to the specific bash tool for this test's echo command to avoid
    // strict-mode violations if the shared test server ends up showing more
    // than one bash invocation.
    const modal = await openToolPill(page, 'echo');
    const bashTool = modal.locator('.bash-tool').filter({ hasText: 'just plain text with no escapes' }).first();
    await expect(bashTool).toBeVisible({ timeout: 15000 });
    const details = bashTool.locator('.bash-tool-details');
    await expect(details).toBeVisible();

    const outputPre = details.locator('.bash-tool-code').last();
    await expect(outputPre).toBeVisible();

    // Plain text should be rendered as a text node, not HTML
    const textContent = await outputPre.textContent();
    expect(textContent).toContain('just plain text with no escapes');

    // Should NOT have <span> tags (plain text path)
    const innerHTML = await outputPre.innerHTML();
    expect(innerHTML).not.toContain('<span');
  });

  test('bash output with cursor-movement ANSI sequences renders no stray letters', async ({ page, request }) => {
    // Regression: the old ansi-to-html library only understood SGR color
    // codes; its catch-all token consumed the introducer + params but left the
    // trailing final byte of cursor-movement sequences. \x1b[1G (Cursor
    // Horizontal Absolute) is emitted by logging libraries to redraw/align
    // status lines, and produced "GGGDev code has changes" with three of them.
    // Shelley pre-strips non-SGR sequences (and now uses ansi_up, which parses
    // complete CSI sequences), so only the readable text remains.
    const slug = await createConversationViaAPI(
      request,
      `bash: printf '\\033[1G\\033[1G\\033[1GDev code has changes not yet deployed'`,
    );

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    const modal = await openToolPill(page, 'printf');
    const bashTool = modal.locator('.bash-tool').filter({ hasText: 'Dev code has changes' }).first();
    await expect(bashTool).toBeVisible({ timeout: 15000 });
    const details = bashTool.locator('.bash-tool-details');
    await expect(details).toBeVisible();

    const outputPre = details.locator('.bash-tool-code').last();
    await expect(outputPre).toBeVisible();

    const textContent = await outputPre.textContent();
    // The readable text is preserved...
    expect(textContent).toContain('Dev code has changes not yet deployed');
    // ...and no stray escape letters or raw fragments leak through.
    expect(textContent).not.toContain('GGG');
    expect(textContent).not.toContain('[1G');
    expect(textContent).not.toContain('\x1b');

    // Cursor-only sequences are non-SGR, so no color <span>s are expected.
    const innerHTML = await outputPre.innerHTML();
    expect(innerHTML).not.toContain('<span');
  });
});
