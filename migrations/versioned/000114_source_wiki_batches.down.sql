DROP INDEX IF EXISTS source_wiki_attempts_batch;
DROP INDEX IF EXISTS source_wiki_one_running_target;
ALTER TABLE source_wiki_attempts DROP COLUMN IF EXISTS batch_id;
ALTER TABLE source_wiki_attempts DROP COLUMN IF EXISTS topic_kind;
ALTER TABLE source_wiki_attempts DROP COLUMN IF EXISTS topic_key;
CREATE UNIQUE INDEX IF NOT EXISTS source_wiki_one_running_module
    ON source_wiki_attempts(tenant_id, knowledge_base_id, source_id, module_path) WHERE status='running';
DROP TABLE IF EXISTS source_wiki_batch_calls;
DROP TABLE IF EXISTS source_wiki_topics;
DROP INDEX IF EXISTS source_wiki_one_running_batch;
DROP INDEX IF EXISTS source_wiki_batches_status_deadline;
DROP INDEX IF EXISTS source_wiki_batches_source_created;
DROP TABLE IF EXISTS source_wiki_batches;
