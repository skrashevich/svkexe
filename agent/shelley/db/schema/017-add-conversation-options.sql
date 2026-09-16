-- Add conversation_options column to conversations
-- This is a JSON column for extensible conversation settings
-- Default is '{}' (empty JSON object)

ALTER TABLE conversations ADD COLUMN conversation_options TEXT NOT NULL DEFAULT '{}';
