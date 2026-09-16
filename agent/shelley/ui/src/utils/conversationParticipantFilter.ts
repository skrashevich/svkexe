import { compareTagGroupKeys } from "./tagFilter";

interface ConversationWithParticipantList {
  parent_conversation_id?: string | null;
  is_draft?: boolean;
  participants?: { email: string; message_count: number }[] | null;
}

function foldEmail(email: string | null | undefined): string {
  return email?.trim().toLowerCase() ?? "";
}

export function hasOtherParticipant(
  conversations: readonly ConversationWithParticipantList[],
  currentUserEmail: string | null | undefined,
): boolean {
  const current = foldEmail(currentUserEmail);
  if (!current) return false;
  return conversations.some((conversation) =>
    conversation.participants?.some(({ email }) => {
      const participant = foldEmail(email);
      return participant !== "" && participant !== current;
    }),
  );
}

// True when some conversation itself has more than one participant. Distinct
// from hasOtherParticipant: two single-user conversations by different people
// satisfy that but not this.
export function hasMultiParticipantConversation(
  conversations: readonly ConversationWithParticipantList[],
): boolean {
  return conversations.some((conversation) => participantEmails(conversation).length > 1);
}

// The folded, sorted, deduped participant emails of a conversation.
function participantEmails(conversation: ConversationWithParticipantList): string[] {
  const folded = new Set(
    conversation.participants?.map(({ email }) => foldEmail(email)).filter(Boolean) ?? [],
  );
  return [...folded].sort();
}

// --- Grouping ---
//
// Mirrors tag grouping (see tagFilter.ts): the key is the whole participant
// set, NUL-joined, so every conversation lands in exactly one group.
const PARTICIPANT_KEY_SEP = "\u0000";

export function participantGroupKey(conversation: ConversationWithParticipantList): string | null {
  const emails = participantEmails(conversation);
  return emails.length === 0 ? null : emails.join(PARTICIPANT_KEY_SEP);
}

export function participantGroupLabel(key: string): string {
  return key.split(PARTICIPANT_KEY_SEP).join(", ");
}

// Groups the current user belongs to come first, then smaller participant
// sets, then the same tuple order as tag groups.
export function compareParticipantGroupKeys(
  a: string,
  b: string,
  currentUserEmail: string | null | undefined,
): number {
  const current = foldEmail(currentUserEmail);
  const left = a.split(PARTICIPANT_KEY_SEP);
  const right = b.split(PARTICIPANT_KEY_SEP);
  const includes = (emails: string[]) => current !== "" && emails.includes(current);
  const partition = Number(includes(right)) - Number(includes(left));
  if (partition !== 0) return partition;
  const size = left.length - right.length;
  return size !== 0 ? size : compareTagGroupKeys(a, b);
}

export function filterConversationsByParticipantQuery<T extends ConversationWithParticipantList>(
  conversations: T[],
  selectedUsers: readonly string[],
  includeUnattributed: boolean,
): T[] {
  const users = new Set(selectedUsers.map(foldEmail).filter(Boolean));
  if (users.size === 0 && !includeUnattributed) return conversations;
  return conversations.filter((conversation) => {
    // Subagents ride with their parent; a draft has no messages yet, so no
    // participants, and must not vanish under the default current-user filter.
    if (conversation.parent_conversation_id || conversation.is_draft) return true;
    const participants =
      conversation.participants?.map(({ email }) => foldEmail(email)).filter(Boolean) ?? [];
    if (includeUnattributed) return users.size === 0 && participants.length === 0;
    const participantSet = new Set(participants);
    return [...users].every((email) => participantSet.has(email));
  });
}
