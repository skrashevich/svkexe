import assert from "node:assert/strict";
import { SLASH_COMMANDS, slashCommandsForConversation } from "./slashCommands";

function commands(child: boolean): string[] {
  return slashCommandsForConversation(child).map((item) => item.command);
}

assert(commands(false).includes(SLASH_COMMANDS.BTW.command), "top-level conversations offer /btw");
assert(!commands(true).includes(SLASH_COMMANDS.BTW.command), "child conversations omit /btw");
assert(commands(true).includes(SLASH_COMMANDS.FORK.command), "child conversations retain commands");
