-- Draft conversations.
--
-- A draft is a conversation with no messages whose body is held in the
-- `draft` text column. The `is_draft` flag distinguishes drafts from
-- regular conversations so the UI can style them and the API can promote
-- them when the user sends their first message (clears draft, flips
-- is_draft=FALSE). Drafts otherwise behave like any other conversation:
-- they appear in the list, can be deleted, and show up in patch diffs.

ALTER TABLE conversations ADD COLUMN is_draft BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE conversations ADD COLUMN draft TEXT NOT NULL DEFAULT '';
