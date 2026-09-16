-- The llm_requests table is no longer used; the debug feature was removed.
-- Dropping the table can be very slow on large databases, so we just abandon
-- it in place rather than dropping it. New code does not read or write it.
SELECT 1;
