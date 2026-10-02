CREATE TABLE source_wiki_batches (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    source_id VARCHAR(36) NOT NULL,
    snapshot_id VARCHAR(36) NOT NULL,
    source_config_fingerprint VARCHAR(64) NOT NULL,
    source_updated_at TIMESTAMPTZ NOT NULL,
    model_id VARCHAR(36) NOT NULL,
    model_settings_fingerprint VARCHAR(64) NOT NULL,
    model_context_window INTEGER NOT NULL DEFAULT 0,
    max_completion_tokens INTEGER NOT NULL,
    status TEXT NOT NULL,
    phase TEXT NOT NULL,
    current_topic_key TEXT NOT NULL DEFAULT '',
    cursor INTEGER NOT NULL DEFAULT 0,
    qa_cursor INTEGER NOT NULL DEFAULT 0,
    publish_cursor INTEGER NOT NULL DEFAULT 0,
    qa_deadline_at TIMESTAMPTZ,
    qa_approved_at TIMESTAMPTZ,
    qa_approval_digest VARCHAR(64) NOT NULL DEFAULT '',
    revalidation_attempt_id VARCHAR(36) NOT NULL DEFAULT '',
    candidate_count INTEGER NOT NULL DEFAULT 0,
    initial_count INTEGER NOT NULL DEFAULT 0,
    calls_reserved INTEGER NOT NULL DEFAULT 0,
    tokens_reserved INTEGER NOT NULL DEFAULT 0,
    skeleton_calls_reserved INTEGER NOT NULL DEFAULT 0,
    skeleton_tokens_reserved INTEGER NOT NULL DEFAULT 0,
    qa_calls_reserved INTEGER NOT NULL DEFAULT 0,
    qa_tokens_reserved INTEGER NOT NULL DEFAULT 0,
    max_calls INTEGER NOT NULL,
    max_tokens INTEGER NOT NULL,
    max_elapsed_ms BIGINT NOT NULL,
    max_initial_topics INTEGER NOT NULL,
    skeleton_max_calls INTEGER NOT NULL,
    skeleton_max_tokens INTEGER NOT NULL,
    qa_max_calls INTEGER NOT NULL,
    qa_max_tokens INTEGER NOT NULL,
    deadline_at TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    CHECK (status IN ('queued', 'running', 'completed', 'failed', 'expired')),
    CHECK (phase IN ('skeleton', 'cards', 'batch_qa', 'publishing', 'finished')),
    CHECK (cursor >= 0 AND candidate_count >= 0 AND initial_count >= 0 AND initial_count <= candidate_count),
    CHECK (qa_cursor >= 0 AND qa_cursor <= initial_count),
    CHECK (publish_cursor >= 0 AND publish_cursor <= initial_count),
    CHECK (max_calls > 0 AND max_calls <= 240),
    CHECK (max_tokens > 0 AND max_tokens <= 4000000),
    CHECK (max_elapsed_ms > 0 AND max_elapsed_ms <= 3600000),
    CHECK (max_initial_topics > 0 AND max_initial_topics <= 40),
    CHECK (max_completion_tokens > 0 AND max_completion_tokens <= 4096),
    CHECK (skeleton_max_calls >= 0 AND skeleton_max_calls <= 6),
    CHECK (skeleton_max_tokens >= 0 AND skeleton_max_tokens <= 120000),
    CHECK (qa_max_calls >= 0 AND qa_max_calls <= 12),
    CHECK (qa_max_tokens >= 0 AND qa_max_tokens <= 240000),
    CHECK (calls_reserved >= 0 AND calls_reserved <= max_calls),
    CHECK (tokens_reserved >= 0 AND tokens_reserved <= max_tokens),
    CHECK (skeleton_calls_reserved >= 0 AND skeleton_calls_reserved <= skeleton_max_calls),
    CHECK (skeleton_tokens_reserved >= 0 AND skeleton_tokens_reserved <= skeleton_max_tokens),
    CHECK (qa_calls_reserved >= 0 AND qa_calls_reserved <= qa_max_calls),
    CHECK (qa_tokens_reserved >= 0 AND qa_tokens_reserved <= qa_max_tokens),
    CHECK (deadline_at > created_at AND deadline_at <= created_at + INTERVAL '60 minutes'),
    CHECK (max_elapsed_ms = (EXTRACT(EPOCH FROM (deadline_at - created_at)) * 1000)::BIGINT)
);
CREATE INDEX source_wiki_batches_source_created
    ON source_wiki_batches(knowledge_base_id, source_id, created_at DESC);
CREATE INDEX source_wiki_batches_status_deadline
    ON source_wiki_batches(status, deadline_at);
CREATE UNIQUE INDEX source_wiki_one_running_batch
    ON source_wiki_batches(tenant_id, knowledge_base_id, source_id)
    WHERE status IN ('queued', 'running');

CREATE TABLE source_wiki_topics (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    source_id VARCHAR(36) NOT NULL,
    topic_key TEXT NOT NULL,
    snapshot_id VARCHAR(36) NOT NULL,
    kind TEXT NOT NULL,
    module_path TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    initial BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL,
    uncertain BOOLEAN NOT NULL DEFAULT FALSE,
    uncertainty_reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    relations JSONB NOT NULL DEFAULT '[]'::jsonb,
    batch_id VARCHAR(36) REFERENCES source_wiki_batches(id) ON DELETE SET NULL,
    attempt_id VARCHAR(36) REFERENCES source_wiki_attempts(id) ON DELETE SET NULL,
    wiki_slug TEXT NOT NULL DEFAULT '',
    last_ready_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (source_id, topic_key),
    CHECK (kind IN ('system', 'module', 'flow')),
    CHECK (status IN ('planned', 'ready', 'draft', 'failed', 'insufficient_evidence', 'expansion')),
    CHECK (jsonb_typeof(uncertainty_reasons) = 'array'),
    CHECK (jsonb_typeof(relations) = 'array')
);
CREATE INDEX source_wiki_topics_coverage
    ON source_wiki_topics(knowledge_base_id, source_id, status, priority DESC);
CREATE INDEX source_wiki_topics_snapshot
    ON source_wiki_topics(source_id, snapshot_id);

CREATE TABLE source_wiki_batch_calls (
    id VARCHAR(36) PRIMARY KEY,
    batch_id VARCHAR(36) NOT NULL REFERENCES source_wiki_batches(id) ON DELETE CASCADE,
    attempt_id VARCHAR(36) REFERENCES source_wiki_attempts(id) ON DELETE CASCADE,
    attempt_call_id VARCHAR(36),
    phase TEXT NOT NULL,
    provider_phase TEXT NOT NULL DEFAULT '',
    reserved_tokens INTEGER NOT NULL,
    expected_qa_cursor INTEGER,
    actual_tokens INTEGER,
    outcome TEXT NOT NULL DEFAULT 'reserved',
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK (phase IN ('skeleton', 'card', 'batch_qa')),
    CHECK (reserved_tokens > 0),
    CHECK (actual_tokens IS NULL OR actual_tokens >= 0),
    CHECK (outcome IN ('reserved', 'succeeded', 'provider_error', 'unknown', 'over_budget')),
    CHECK ((attempt_id IS NULL) OR (phase = 'card')),
    CHECK ((attempt_call_id IS NULL) OR (attempt_id IS NOT NULL))
);
CREATE UNIQUE INDEX source_wiki_batch_call_attempt_identity
    ON source_wiki_batch_calls(attempt_call_id) WHERE attempt_call_id IS NOT NULL;
CREATE INDEX source_wiki_batch_calls_batch_phase
    ON source_wiki_batch_calls(batch_id, phase, created_at);

ALTER TABLE source_wiki_attempts
    ADD COLUMN IF NOT EXISTS batch_id VARCHAR(36) REFERENCES source_wiki_batches(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS topic_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS topic_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS staged_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS staged_page_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS result_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE source_wiki_attempts DROP CONSTRAINT IF EXISTS source_wiki_attempts_result_kind_check;
ALTER TABLE source_wiki_attempts ADD CONSTRAINT source_wiki_attempts_result_kind_check
    CHECK (result_kind IN ('', 'insufficient_evidence'));
CREATE INDEX source_wiki_attempts_batch
    ON source_wiki_attempts(batch_id, created_at);
DROP INDEX IF EXISTS source_wiki_one_running_module;
CREATE UNIQUE INDEX source_wiki_one_running_target
    ON source_wiki_attempts(tenant_id, knowledge_base_id, source_id,
        COALESCE(NULLIF(batch_id, ''), 'manual'), COALESCE(NULLIF(topic_key, ''), module_path))
    WHERE status = 'running';
