-- name: CreateMessage :one
-- created_at is normally left NULL to default to CURRENT_TIMESTAMP; see
-- db.CreateMessageParams.CreatedAt for who overrides it and why.
INSERT INTO messages (message_id, conversation_id, sequence_id, generation, type, llm_data, user_data, usage_data, display_data, excluded_from_context, llm_api_url, model_name, user_email, other_usage_data, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(sqlc.narg('created_at'), CURRENT_TIMESTAMP))
RETURNING *;

-- name: GetNextSequenceID :one
SELECT COALESCE(MAX(sequence_id), 0) + 1 
FROM messages 
WHERE conversation_id = ?;

-- name: GetMaxSequenceIDsForAllConversations :many
SELECT conversation_id, CAST(COALESCE(MAX(sequence_id), 0) AS INTEGER) AS max_sequence_id
FROM messages
GROUP BY conversation_id;

-- name: GetMessage :one
SELECT * FROM messages
WHERE message_id = ?;

-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id ASC;

-- name: ListMessagesForContext :many
SELECT m.* FROM messages m
INNER JOIN conversations c ON m.conversation_id = c.conversation_id
WHERE m.conversation_id = ?
  AND m.excluded_from_context = FALSE
  AND m.generation = c.current_generation
ORDER BY m.sequence_id ASC;

-- name: ListMessagesPaginated :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id ASC
LIMIT ? OFFSET ?;

-- name: GetGenerationAtOrBeforeSequence :one
-- Returns the generation of the last message at or before a sequence_id.
-- Used by fork to copy the generation that was active at the fork point,
-- which may be older than the conversation's current_generation.
--
-- Slug markers are excluded, matching CopyMessagesForFork. A marker is stamped
-- with whatever current_generation holds when the (racing, 15s-timeout) slug
-- goroutine lands, so a marker written after a compaction bumped the generation
-- would otherwise answer this question with the new generation and make the fork
-- copy the wrong one.
SELECT generation FROM messages
WHERE conversation_id = ? AND sequence_id <= ? AND type != 'slug'
ORDER BY sequence_id DESC LIMIT 1;

-- name: CopyMessagesForFork :exec
-- Copies the messages of a source conversation's given generation, up to and
-- including a cutoff sequence_id, into a destination conversation. The copies
-- are renumbered to generation 1 (the destination starts a fresh generation
-- history), get new message_ids, and preserve content, ordering, and original
-- timestamps. Used to fork a conversation.
INSERT INTO messages (message_id, conversation_id, sequence_id, generation, type, llm_data, user_data, usage_data, display_data, excluded_from_context, llm_api_url, model_name, user_email, forked_from_message_id, created_at, other_usage_data)
SELECT lower(hex(randomblob(16))), sqlc.arg('dest_conversation_id'), m.sequence_id, 1, m.type, m.llm_data, m.user_data, m.usage_data, m.display_data, m.excluded_from_context, m.llm_api_url, m.model_name, m.user_email, m.message_id, m.created_at, m.other_usage_data
FROM messages m
WHERE m.conversation_id = sqlc.arg('source_conversation_id')
  AND m.sequence_id <= sqlc.arg('cutoff_sequence_id')
  AND m.generation = sqlc.arg('source_generation')
  -- Skip slug markers: a fork derives its slug from the source's synchronously,
  -- with no LLM call, so copying the marker would re-report a cost the fork
  -- never incurred. Leaving a hole in the copied sequence is fine here: this
  -- already copies a single generation while preserving source sequence_ids, so
  -- forks legitimately have gaps and clients must already tolerate them.
  AND m.type != 'slug'
ORDER BY m.sequence_id ASC;

-- name: ListMessagesByType :many
SELECT * FROM messages
WHERE conversation_id = ? AND type = ?
ORDER BY sequence_id ASC;

-- name: GetLatestActionableMessage :one
-- The latest message a user could act on. Slug markers are excluded: they hold
-- only the slug LLM call's usage, render as nothing, and land at an arbitrary
-- point (the call races the first turn). Callers gate the Retry/Continue
-- affordances on this row's type being 'error', so a trailing slug marker would
-- otherwise silently disable them.
SELECT * FROM messages
WHERE conversation_id = ? AND type != 'slug'
ORDER BY sequence_id DESC
LIMIT 1;

-- name: DeleteMessage :exec
DELETE FROM messages
WHERE message_id = ?;

-- name: DeleteConversationMessages :exec
DELETE FROM messages
WHERE conversation_id = ?;

-- name: CountMessagesInConversation :one
SELECT COUNT(*) FROM messages
WHERE conversation_id = ?;

-- name: CountMessagesByType :one
SELECT COUNT(*) FROM messages
WHERE conversation_id = ? AND type = ?;

-- name: CountConsecutiveMessagesByType :one
-- Counts the trailing run of messages of the given type, i.e. those after the
-- last message of any OTHER type. Used to cap consecutive retry warnings.
--
-- Slug markers don't break a run: they are bookkeeping rows carrying only the
-- cost of the LLM call that named the conversation, they render as nothing, and
-- slug generation races the first turn, so a marker landing in the middle of a
-- retry-warning storm would reset the counter and let a fresh batch of warnings
-- through.
SELECT COUNT(*) FROM messages m
WHERE m.conversation_id = sqlc.arg('conversation_id')
  AND m.generation = sqlc.arg('generation')
  AND m.type = sqlc.arg('type')
  AND m.sequence_id > COALESCE(
    (SELECT MAX(prev.sequence_id) FROM messages prev
     WHERE prev.conversation_id = sqlc.arg('conversation_id')
       AND prev.generation = sqlc.arg('generation')
       AND prev.type != sqlc.arg('type')
       AND prev.type != 'slug'),
    0);

-- name: ListMessagesTail :many
-- Returns the last N messages in ascending order. If fewer than N
-- exist, returns all of them.
--
-- N counts VISIBLE messages, but slug markers inside the resulting window are
-- still returned. Two reasons to do it this way rather than just excluding them:
-- an invisible bookkeeping row must not eat the window (?tail=1 would hand back
-- nothing displayable), yet dropping markers from the middle would punch holes
-- in the sequence space, and clients that detect lost messages by sequence
-- contiguity (see the iOS store's firstGapBoundary) would read those holes as
-- missing data and re-fetch forever.
SELECT m.* FROM messages m
WHERE m.conversation_id = sqlc.arg('conversation_id')
  AND m.sequence_id >= COALESCE((
    SELECT MIN(v.sequence_id) FROM (
      SELECT vis.sequence_id FROM messages vis
      WHERE vis.conversation_id = sqlc.arg('conversation_id')
        AND vis.type != 'slug'
      ORDER BY vis.sequence_id DESC
      LIMIT sqlc.arg('limit')
    ) v
  ), 0)
ORDER BY m.sequence_id ASC;

-- name: ListMessagesSince :many
-- The client cache-repair path (?last_sequence_id=N). Must NOT filter any type:
-- it is what heals a client whose view of the sequence space has a hole, so it
-- has to be able to deliver every row, markers included. Deliberately different
-- from ListMessagesTail (a display window, which counts visible rows); don't
-- "make these consistent".
SELECT * FROM messages
WHERE conversation_id = ? AND sequence_id > ?
ORDER BY sequence_id ASC;

-- name: UpdateMessageUserData :exec
-- Mutating message rows is forbidden in production paths (messages are an
-- immutable, append-only log keyed by sequence_id and cached in the browser).
-- This UPDATE exists ONLY for the FTS-trigger test (TestMessages*), which
-- verifies the messages_fts AFTER UPDATE trigger re-indexes user_data.
UPDATE messages SET user_data = ? WHERE message_id = ?;

-- name: ListAgentMessagesSinceLastUser :many
-- Returns the agent messages produced during the most recent user turn,
-- ordered newest-first. "Most recent user turn" = all agent messages
-- whose sequence_id is greater than the sequence_id of the most recent
-- user message (or all agent messages if there is no user message yet).
-- Used by the end-of-turn
-- notification builder to pick a useful body line.
SELECT m.message_id, m.conversation_id, m.sequence_id, m.type,
       m.llm_data, m.user_data, m.usage_data, m.created_at,
       m.display_data, m.excluded_from_context, m.generation,
       m.llm_api_url, m.model_name, m.forked_from_message_id, m.user_email,
       m.other_usage_data
FROM messages m
WHERE m.conversation_id = ? AND m.type = 'agent'
  AND m.sequence_id > COALESCE(
    (SELECT MAX(u.sequence_id) FROM messages u
     WHERE u.conversation_id = ? AND u.type = 'user'),
    0)
ORDER BY m.sequence_id DESC;
