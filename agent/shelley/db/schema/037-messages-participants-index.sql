-- Index for the conversation-list participants column, which collects the
-- distinct exe.dev authors (messages.user_email) of each listed conversation
-- so clients can filter the list to "my" conversations.
--
-- Partial + covering: only user messages from authenticated requests carry a
-- user_email, so the index holds one entry per authored message and the
-- planner answers the correlated subquery straight from the index without
-- touching the messages table.
CREATE INDEX IF NOT EXISTS idx_messages_participants
  ON messages(conversation_id, user_email)
  WHERE user_email IS NOT NULL AND user_email <> '';

-- Seed the planner's on-disk statistics for the new index. Cheap: ANALYZE on
-- a single partial index only walks that index's entries (one per authored
-- message). It is also DDL, so it bumps the schema cookie and forces
-- already-open connections to reload their cached stats.
ANALYZE idx_messages_participants;
