-- Remove the messages.type CHECK constraint.
-- Message types are an application-level enum; the database intentionally
-- does not enumerate them so adding a new UI-only type does not require a
-- table rebuild migration.

DROP TRIGGER messages_fts_ai;
DROP TRIGGER messages_fts_ad;
DROP TRIGGER messages_fts_au;

CREATE TABLE messages_new (
    message_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    sequence_id INTEGER NOT NULL,
    type TEXT NOT NULL,
    llm_data TEXT,
    user_data TEXT,
    usage_data TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    display_data TEXT,
    excluded_from_context BOOLEAN NOT NULL DEFAULT FALSE,
    generation INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (conversation_id) REFERENCES conversations(conversation_id) ON DELETE CASCADE
);

INSERT INTO messages_new (message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data, created_at, display_data, excluded_from_context, generation)
SELECT message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data, created_at, display_data, excluded_from_context, generation FROM messages;

DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;

CREATE INDEX idx_messages_conversation_id ON messages(conversation_id);
CREATE INDEX idx_messages_conversation_sequence ON messages(conversation_id, sequence_id);
CREATE INDEX idx_messages_type ON messages(type);
CREATE INDEX idx_messages_conversation_generation_context_sequence ON messages(conversation_id, generation, excluded_from_context, sequence_id);

DELETE FROM messages_fts;
INSERT INTO messages_fts(rowid, text)
SELECT m.rowid, (
    SELECT group_concat(t.value, ' ')
    FROM json_tree(coalesce(m.user_data, m.llm_data)) t
    WHERE t.key IN ('Text', 'Thinking')
      AND t.type = 'text'
      AND length(t.value) > 0
)
FROM messages m
WHERE m.type IN ('user', 'agent');

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages
WHEN new.type IN ('user', 'agent') BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (
        new.rowid,
        (SELECT group_concat(t.value, ' ')
         FROM json_tree(coalesce(new.user_data, new.llm_data)) t
         WHERE t.key IN ('Text', 'Thinking')
           AND t.type = 'text'
           AND length(t.value) > 0)
    );
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages
WHEN old.type IN ('user', 'agent') BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid;
END;

CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages
WHEN old.type IN ('user', 'agent') OR new.type IN ('user', 'agent') BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid;
    INSERT INTO messages_fts(rowid, text)
    SELECT new.rowid, (
        SELECT group_concat(t.value, ' ')
        FROM json_tree(coalesce(new.user_data, new.llm_data)) t
        WHERE t.key IN ('Text', 'Thinking')
          AND t.type = 'text'
          AND length(t.value) > 0
    )
    WHERE new.type IN ('user', 'agent');
END;
