-- Record fork provenance on copied messages.
--
-- When a conversation is forked, its messages are copied into the new
-- conversation with fresh message_ids (see CopyMessagesForFork). Without a
-- back-reference, summing usage_data / llm_api_url / model_name across all
-- messages double-counts the LLM work that was already incurred in the source
-- conversation, because the forked copies carry identical usage values.
--
-- forked_from_message_id points at the source message a copy was made from.
-- It is NULL for originally-incurred messages. To compute usage that counts
-- each LLM call exactly once, filter WHERE forked_from_message_id IS NULL.
-- It deliberately has no FK constraint: the source message (or its whole
-- conversation) may be deleted later, and the fork should survive as an
-- independent conversation rather than cascade-delete or block.
ALTER TABLE messages ADD COLUMN forked_from_message_id TEXT;

-- Index so attributing a fork's copies back to their origins (or finding all
-- forks of a message) is cheap.
CREATE INDEX idx_messages_forked_from ON messages(forked_from_message_id) WHERE forked_from_message_id IS NOT NULL;
