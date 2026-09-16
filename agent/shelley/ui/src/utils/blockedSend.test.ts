// Contract test for the "send blocked because no model" path.
//
// MessageInput clears the textarea optimistically and only restores it in its
// catch block (see handleSubmit / handleQueueMessage / handleSendNow: "Keep the
// message on error so user can retry"). So a guard that merely sets an error
// and RETURNS looks like success to MessageInput, which then clears the
// composer AND the cached draft mirror — silently destroying what the user
// typed at the exact moment they cannot send it.
//
// This models that contract: the send handler must REJECT so the caller's
// catch restores the text.
import { canSendWithModel, needsModel } from "./modelSetupHint";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

// A stand-in for MessageInput's optimistic-clear contract.
async function submitLikeMessageInput(
  text: string,
  onSend: (m: string) => Promise<void>,
): Promise<{ composer: string; cleared: boolean }> {
  let composer = text;
  let cleared = false;
  try {
    await onSend(text);
    composer = "";
    cleared = true;
  } catch {
    // Keep the message on error so the user can retry.
  }
  return { composer, cleared };
}

// The guard as it must behave: throw, so the composer keeps the text.
async function guardedSend(text: string, ready: readonly string[], model: string) {
  if (!canSendWithModel(model, ready) && needsModel(text)) {
    throw new Error("No AI models available");
  }
}

const run = async () => {
  // No models: the text must survive and the draft must NOT be cleared.
  {
    const r = await submitLikeMessageInput("a half-written thought", (m) => guardedSend(m, [], ""));
    check("blocked send keeps the composer text", r.composer === "a half-written thought", r);
    check("blocked send does not clear the draft", r.cleared === false, r);
  }
  // Stale model id with a non-empty catalog: same contract.
  {
    const r = await submitLikeMessageInput("another thought", (m) =>
      guardedSend(m, ["claude-opus-4.8"], "claude-fable-5"),
    );
    check("stale-model send keeps the composer text", r.composer === "another thought", r);
  }
  // Healthy: normal clear-on-success still happens.
  {
    const r = await submitLikeMessageInput("hello", (m) =>
      guardedSend(m, ["claude-opus-4.8"], "claude-opus-4.8"),
    );
    check("successful send clears the composer", r.composer === "" && r.cleared, r);
  }
  // Model-free commands stay sendable even with no model at all.
  {
    const r = await submitLikeMessageInput("/diff", (m) => guardedSend(m, [], ""));
    check("model-free command is not blocked", r.cleared === true, r);
  }

  if (failed > 0) {
    console.error(failures.join("\n"));
    console.error(`\n${passed} passed, ${failed} failed`);
    process.exit(1);
  }
  console.log(`${passed} passed`);
};
void run();
