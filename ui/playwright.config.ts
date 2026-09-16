import { defineConfig, devices } from "@playwright/test";

/**
 * The test server is managed by globalSetup (scripts/global-setup.ts).
 * It starts shelley with --port 0 and exports the actual URL via
 * PLAYWRIGHT_TEST_BASE_URL, which Playwright's baseURL fixture reads
 * automatically. This eliminates hardcoded ports and port conflicts.
 *
 * To point at an already-running server, set TEST_SERVER_URL.
 *
 * @see https://playwright.dev/docs/test-configuration
 */
export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./scripts/global-setup.ts",
  /* Run tests in files in parallel */
  fullyParallel: true,
  /* Fail the build on CI if you accidentally left test.only in the source code. */
  forbidOnly: !!process.env.CI,
  /* Retry on CI only */
  retries: process.env.CI ? 1 : 0,
  /* Per-test timeout. Playwright's 30s default is too tight for our flows:
   * several specs drive multiple agent round-trips, each guarded by its own
   * 30s expect() wait, so the whole test legitimately needs more than 30s
   * under CI load. When the test timeout fired mid-assertion Playwright tore
   * down the page, surfacing as "Target page, context or browser has been
   * closed" rather than a clear timeout. 120s gives ample headroom over the
   * realistic few-seconds runtime while still bounding a genuinely hung test. */
  timeout: 120_000,
  /* Use 2 workers in CI. All tests share a single predictable-mode server
   * backed by a single-writer SQLite DB; 3 workers caused systemic flake
   * from SSE/agent-working contention on ubuntu-latest. 2 keeps wall-clock
   * low while avoiding the overload that made every run drop ~1 test. */
  workers: process.env.CI ? 2 : 1,
  /* Reporter to use. See https://playwright.dev/docs/test-reporters */
  reporter: process.env.CI ? [["html", { open: "never" }], ["list"]] : "list",
  /* Shared settings for all the projects below. See https://playwright.dev/docs/api/class-testoptions. */
  use: {
    /* baseURL is set automatically via PLAYWRIGHT_TEST_BASE_URL from global-setup */
    /* Collect trace on all tests, keep only on failure */
    trace: "retain-on-failure",
    /* Take a screenshot after every test */
    screenshot: "on",
    /* Record video on all tests, keep only on failure */
    video: "retain-on-failure",
    /* Ask the app for reduced motion. Playwright waits for an element to stop
       moving before it clicks, so UI animations are paid for on every single
       interaction -- PrimeVue's 0.3s dialog enter/leave alone cost ~0.6s per
       modal open/close, which was ~10s of tool-components.spec.ts. styles.css
       honours the preference (and reduced-motion.spec.ts checks that it does,
       without freezing spinners), so the suite gets the same DOM without the
       waiting.

       This has to go through contextOptions. The top-level `reducedMotion`
       option is accepted and even reported by testInfo.project.use, but as of
       Playwright 1.60 it does not reach the context behind the `page` fixture:
       matchMedia('(prefers-reduced-motion: reduce)') stays false. colorScheme
       set the same way does apply, and a hand-rolled browser.newContext({
       reducedMotion: 'reduce' }) works, so it is specific to this option on the
       fixture path. contextOptions is passed through verbatim and does work.
       reduced-motion.spec.ts asserts the emulation is actually in effect, so if
       a future upgrade fixes or breaks this, a test says so rather than the
       suite quietly going slow again. */
    contextOptions: { reducedMotion: "reduce" },
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Pixel 5"] },
    },
    // Firefox is opt-in (PW_FIREFOX=1) rather than part of the default CI
    // matrix: most specs were authored against Chromium and would flake under
    // an untuned Firefox run. It exists so scroll/layout regressions that are
    // easier to trigger in Gecko (e.g. content-visibility height estimates)
    // can be reproduced locally with `PW_FIREFOX=1 pnpm run test:e2e`.
    ...(process.env.PW_FIREFOX
      ? [
          {
            name: "firefox",
            use: { ...devices["Desktop Firefox"] },
          },
        ]
      : []),
    // WebKit is opt-in (PW_WEBKIT=1) for the same reason as Firefox, and it is
    // the only way to reproduce Safari-specific scroll bugs here. It differs
    // from Chromium in a way that matters to autoscroll: Chromium's scroll
    // anchoring restores the offset losslessly when content above the viewport
    // collapses and then grows back, and WebKit's does not (measured: 5000 ->
    // 3700 -> 5700 across a 2000px collapse/regrow). Layout bugs that Chromium
    // silently absorbs are therefore user-visible on Safari.
    ...(process.env.PW_WEBKIT
      ? [
          {
            name: "webkit",
            use: { ...devices["Desktop Safari"] },
          },
        ]
      : []),
  ],
});
