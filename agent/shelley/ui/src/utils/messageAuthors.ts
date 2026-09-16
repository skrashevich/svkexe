import type { Message } from "../types";

// hasMultipleUsers reports whether more than one distinct exe.dev user has
// participated in a conversation. Only non-empty user_email values count as
// distinct participants: empty strings (unauthenticated/direct access) are
// ignored, so a mix of empty and a single real email is still one participant.
// When true, the UI labels each user message with its author's email.
export function hasMultipleUsers(messages: Pick<Message, "user_email">[]): boolean {
  const emails = new Set<string>();
  for (const m of messages) {
    if (m.user_email) {
      emails.add(m.user_email);
      if (emails.size > 1) return true;
    }
  }
  return false;
}
