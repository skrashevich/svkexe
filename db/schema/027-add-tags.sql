-- Add tags column to conversations. Stored as a JSON array of strings.
ALTER TABLE conversations ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
