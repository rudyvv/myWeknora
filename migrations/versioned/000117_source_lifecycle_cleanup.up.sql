ALTER TABLE data_sources
    ADD COLUMN source_binding_state varchar(16) NOT NULL DEFAULT 'bound'
        CHECK (source_binding_state IN ('bound','unbound')),
    ADD COLUMN source_query_enabled boolean NOT NULL DEFAULT true;

CREATE TABLE source_cleanup_operations (
    id varchar(36) PRIMARY KEY,
    data_source_id varchar(36) NOT NULL UNIQUE REFERENCES data_sources(id),
    tenant_id bigint NOT NULL,
    knowledge_base_id varchar(36) NOT NULL,
    scope varchar(32) NOT NULL CHECK (scope = 'current_and_history'),
    status varchar(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','failed','completed')),
    retryable boolean NOT NULL DEFAULT false,
    error_code varchar(64) NOT NULL DEFAULT '',
    phase varchar(32) NOT NULL DEFAULT 'current_wiki',
    cursor jsonb NOT NULL DEFAULT '{}'::jsonb,
    fencing_token bigint NOT NULL DEFAULT 0,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_owner varchar(128),
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX source_cleanup_operations_recovery
    ON source_cleanup_operations(status, lease_expires_at, updated_at)
    WHERE status IN ('pending','running','failed');
