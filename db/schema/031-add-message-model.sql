-- Record which LLM produced each message's usage data.
--
-- llm_api_url is the URL of the LLM API endpoint the request was sent to
-- (e.g. https://api.anthropic.com/v1/messages). model_name is the model
-- the provider reported handling the request (e.g. claude-opus-4-7).
--
-- Both are nullable: only assistant/agent messages carry usage data, and
-- older rows predate this column. The values mirror the model/url metadata
-- already folded into the usage_data JSON, promoted to first-class columns
-- so they can be queried directly.
ALTER TABLE messages ADD COLUMN llm_api_url TEXT;
ALTER TABLE messages ADD COLUMN model_name TEXT;
