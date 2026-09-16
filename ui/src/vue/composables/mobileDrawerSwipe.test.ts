import { JSDOM } from "jsdom";
import {
  eventPathHasHorizontalScrollContainer,
  hasHorizontalScrollContainer,
  hasOpenModalOverlay,
} from "./mobileDrawerSwipe";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

const dom = new JSDOM("<main><div id='wide'><code id='target'></code></div></main>");
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  Element: dom.window.Element,
});

const wide = document.querySelector("#wide") as HTMLElement;
const target = document.querySelector("#target") as HTMLElement;

Object.defineProperties(wide, {
  clientWidth: { value: 320, configurable: true },
  scrollWidth: { value: 640, configurable: true },
});

run("detects a horizontally scrollable ancestor", () => {
  wide.style.overflowX = "auto";
  assert(hasHorizontalScrollContainer(target), "wide code content should own the swipe");
});

run("ignores an ancestor whose content fits", () => {
  Object.defineProperty(wide, "scrollWidth", { value: 320, configurable: true });
  assert(
    !hasHorizontalScrollContainer(target),
    "non-overflowing content should allow drawer swipes",
  );
});

run("ignores clipped overflow", () => {
  Object.defineProperty(wide, "scrollWidth", { value: 640, configurable: true });
  wide.style.overflowX = "hidden";
  assert(!hasHorizontalScrollContainer(target), "hidden overflow is not horizontally scrollable");
});

run("detects a horizontal scroller inside a patch diff shadow tree", () => {
  const diffsContainer = document.createElement("diffs-container");
  const shadowRoot = diffsContainer.attachShadow({ mode: "open" });
  const scrollContainer = document.createElement("div");
  const diffLine = document.createElement("span");
  scrollContainer.style.overflowX = "auto";
  scrollContainer.append(diffLine);
  shadowRoot.append(scrollContainer);
  document.body.append(diffsContainer);

  Object.defineProperties(scrollContainer, {
    clientWidth: { value: 320, configurable: true },
    scrollWidth: { value: 640, configurable: true },
  });

  assert(
    !hasHorizontalScrollContainer(diffsContainer),
    "retargeted shadow host should not expose its internal scroller",
  );
  assert(
    eventPathHasHorizontalScrollContainer([
      diffLine,
      scrollContainer,
      shadowRoot,
      diffsContainer,
      document,
    ]),
    "the composed event path should preserve patch diff scrolling",
  );
});

run("detects accessible modal dialogs", () => {
  const root = document.createElement("div");
  root.innerHTML = '<div aria-modal="true"></div>';
  assert(hasOpenModalOverlay(root), "aria-modal dialogs should block drawer swipes");
});

run("ignores non-modal dialogs", () => {
  const root = document.createElement("div");
  root.innerHTML = '<div role="dialog" aria-modal="false"></div>';
  assert(!hasOpenModalOverlay(root), "non-modal dialogs should not block drawer swipes");
});

for (const className of [
  "diff-viewer-overlay",
  "image-comment-overlay",
  "command-palette-overlay",
]) {
  run(`detects the ${className} fullscreen overlay`, () => {
    const root = document.createElement("div");
    root.innerHTML = `<div class="${className}"></div>`;
    assert(hasOpenModalOverlay(root), `${className} should block drawer swipes`);
  });
}

console.log("\nmobileDrawerSwipe tests passed");
