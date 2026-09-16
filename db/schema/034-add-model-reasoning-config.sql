-- Configure whether a custom model supports reasoning and how Shelley's
-- generic reasoning levels map to the levels accepted by that model.
ALTER TABLE models ADD COLUMN reasoning_support TEXT NOT NULL DEFAULT 'auto';
ALTER TABLE models ADD COLUMN reasoning_map TEXT NOT NULL DEFAULT '';
