DROP INDEX IF EXISTS source_wiki_attempts_batch;
ALTER TABLE source_wiki_attempts DROP COLUMN IF EXISTS batch_id;
DROP TABLE IF EXISTS source_wiki_batch_calls;
DROP TABLE IF EXISTS source_wiki_topics;
DROP INDEX IF EXISTS source_wiki_one_running_batch;
DROP INDEX IF EXISTS source_wiki_batches_status_deadline;
DROP INDEX IF EXISTS source_wiki_batches_source_created;
DROP TABLE IF EXISTS source_wiki_batches;
