-- Clean up databases that ran the earlier (unreleased) 036-llm-usage.sql
-- migration, which stored indirect LLM usage in a separate table. That design
-- lost fork provenance and forced consumers to union two sources; indirect
-- usage now lives on the affiliated message row (below). Migrations are
-- tracked by filename, so this file deliberately has a different name.
DROP TABLE IF EXISTS llm_usage;

-- Usage from indirect LLM calls affiliated with this message: compaction
-- summarization for a summary message, LLM-backed tools (keyword_search,
-- llm_one_shot, tool install validation, subagent progress summaries) for a
-- tool-result message, slug generation for the conversation's first user
-- message. A JSON array of llm.PurposedUsage objects (llm.Usage fields plus
-- "purpose") — an array because one message can host several calls by
-- different models. NULL when the message has none.
ALTER TABLE messages ADD COLUMN other_usage_data TEXT;
