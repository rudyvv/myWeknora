-- Source-only code terms are a separately indexed projection. They never
-- rewrite chunks, evidence, or the T12-budgeted embedding text.
CREATE TABLE IF NOT EXISTS source_chunk_search_terms (
    chunk_id VARCHAR(36) PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    full_identifiers TEXT[] NOT NULL DEFAULT '{}',
    normalized_terms TEXT[] NOT NULL DEFAULT '{}',
    search_version TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS source_chunk_search_terms_full_gin
    ON source_chunk_search_terms USING GIN (full_identifiers);
CREATE INDEX IF NOT EXISTS source_chunk_search_terms_terms_gin
    ON source_chunk_search_terms USING GIN (normalized_terms);

-- Upgrade existing snapshots in bounded, repeatable batches. All inputs are
-- immutable source path, parser evidence, and original chunk bytes; this does
-- not read embeddings.content or call an embedding model.
DO $$
DECLARE
    inserted_rows INTEGER;
BEGIN
    LOOP
        WITH batch AS MATERIALIZED (
            SELECT c.id, c.content, c.metadata, sm.path, sv.facts, sv.symbols
            FROM source_chunk_references sc
            JOIN chunks c ON c.id=sc.chunk_id
            JOIN source_snapshot_members sm
              ON sm.snapshot_id=sc.snapshot_id
             AND sm.source_file_id=sc.source_file_id
             AND sm.file_version_id=sc.file_version_id
             AND sm.status='parsed'
            JOIN source_file_versions sv
              ON sv.id=sc.file_version_id
             AND sv.source_file_id=sc.source_file_id
             AND sv.snapshot_id=sc.snapshot_id
            WHERE NOT EXISTS (
                SELECT 1 FROM source_chunk_search_terms existing
                WHERE existing.chunk_id=c.id
                  AND existing.search_version='source-code-search-terms-v1'
            )
            ORDER BY c.id
            LIMIT 1000
        ), projected AS (
            SELECT b.id, b.path, b.content,
                   ARRAY(
                       SELECT trim(identifier.value)
                       FROM (
                           SELECT b.path AS value
                           UNION ALL
                           SELECT jsonb_array_elements_text(
                               COALESCE(b.metadata::jsonb #> '{source,symbols}', '[]'::jsonb)
                           )
                           UNION ALL
                           SELECT COALESCE(NULLIF(symbol->>'qualified_name', ''), symbol->>'name')
                           FROM jsonb_array_elements(COALESCE(b.symbols, '[]'::jsonb)) symbol
                           WHERE COALESCE((symbol->'range'->>'start_byte')::INTEGER, -1)
                                   < COALESCE((b.metadata #>> '{source,range,end_byte}')::INTEGER, -1)
                             AND COALESCE((symbol->'range'->>'end_byte')::INTEGER, -1)
                                   > COALESCE((b.metadata #>> '{source,range,start_byte}')::INTEGER, 2147483647)
                           UNION ALL
                           SELECT symbol->>'name'
                           FROM jsonb_array_elements(COALESCE(b.symbols, '[]'::jsonb)) symbol
                           WHERE COALESCE((symbol->'range'->>'start_byte')::INTEGER, -1)
                                   < COALESCE((b.metadata #>> '{source,range,end_byte}')::INTEGER, -1)
                             AND COALESCE((symbol->'range'->>'end_byte')::INTEGER, -1)
                                   > COALESCE((b.metadata #>> '{source,range,start_byte}')::INTEGER, 2147483647)
                           UNION ALL
                           SELECT fact->>'name'
                           FROM jsonb_array_elements(COALESCE(b.facts, '[]'::jsonb)) fact
                           WHERE fact->>'kind' IN (
                               'mybatis_statement','mybatis_result_map','mybatis_sql_fragment','java_mapper_method'
                           )
                             AND COALESCE((fact->'range'->>'start_byte')::INTEGER, -1)
                                   < COALESCE((b.metadata #>> '{source,range,end_byte}')::INTEGER, -1)
                             AND COALESCE((fact->'range'->>'end_byte')::INTEGER, -1)
                                   > COALESCE((b.metadata #>> '{source,range,start_byte}')::INTEGER, 2147483647)
                           UNION ALL
                           SELECT CASE
                               WHEN fact->>'kind'='mybatis_mapper'
                                   THEN NULLIF(fact->>'namespace', '')
                               WHEN fact->>'kind' IN ('mybatis_statement','mybatis_result_map','mybatis_sql_fragment')
                                   THEN NULLIF(fact->>'namespace', '') || '#' || NULLIF(fact->>'name', '')
                               WHEN fact->>'kind'='java_mapper_method'
                                   THEN NULLIF(fact->>'namespace', '') || '#' || NULLIF(fact->>'name', '')
                               ELSE NULL
                           END
                           FROM jsonb_array_elements(COALESCE(b.facts, '[]'::jsonb)) fact
                           WHERE fact->>'kind' IN (
                               'mybatis_mapper','mybatis_statement','mybatis_result_map',
                               'mybatis_sql_fragment','java_mapper_method'
                           )
                             AND COALESCE((fact->'range'->>'start_byte')::INTEGER, -1)
                                 < COALESCE((b.metadata #>> '{source,range,end_byte}')::INTEGER, -1)
                             AND COALESCE((fact->'range'->>'end_byte')::INTEGER, -1)
                                 > COALESCE((b.metadata #>> '{source,range,start_byte}')::INTEGER, 2147483647)
                       ) identifier
                       WHERE identifier.value IS NOT NULL
                         AND trim(identifier.value) <> ''
                         AND char_length(identifier.value) <= 512
                       GROUP BY trim(identifier.value)
                       ORDER BY CASE
                           WHEN trim(identifier.value)=b.path THEN 0
                           WHEN strpos(trim(identifier.value), '#') > 0 THEN 1
                           ELSE 2
                       END, trim(identifier.value)
                       LIMIT 256
                   ) AS full_identifiers
            FROM batch b
        )
        INSERT INTO source_chunk_search_terms
            (chunk_id, path, full_identifiers, normalized_terms, search_version)
        SELECT p.id, p.path, p.full_identifiers,
               ARRAY(
                   SELECT term
                   FROM (
                       SELECT lower(btrim(token, '_$')) AS term, 1 AS priority
                       FROM unnest(p.full_identifiers) identifier(value)
                       CROSS JOIN LATERAL regexp_split_to_table(identifier.value, '[^A-Za-z0-9$]+') AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 1 AS priority
                       FROM unnest(p.full_identifiers) identifier(value)
                       CROSS JOIN LATERAL regexp_split_to_table(identifier.value, '[^A-Za-z0-9_$]+') AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 1 AS priority
                       FROM unnest(p.full_identifiers) identifier(value)
                       CROSS JOIN LATERAL regexp_split_to_table(
                           regexp_replace(
                               regexp_replace(identifier.value, '([a-z0-9])([A-Z])', '\1 \2', 'g'),
                               '([A-Z]+)([A-Z][a-z])', '\1 \2', 'g'
                           ),
                           '[^A-Za-z0-9$]+'
                       ) AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 0 AS priority
                       FROM regexp_split_to_table(p.path, '[^A-Za-z0-9$]+') AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 0 AS priority
                       FROM regexp_split_to_table(p.path, '[^A-Za-z0-9_$]+') AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 0 AS priority
                       FROM regexp_split_to_table(
                           regexp_replace(
                               regexp_replace(p.path, '([a-z0-9])([A-Z])', '\1 \2', 'g'),
                               '([A-Z]+)([A-Z][a-z])', '\1 \2', 'g'
                           ),
                           '[^A-Za-z0-9$]+'
                       ) AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 2 AS priority
                       FROM regexp_split_to_table(p.content, '[^A-Za-z0-9$]+') AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 2 AS priority
                       FROM regexp_split_to_table(p.content, '[^A-Za-z0-9_$]+') AS pieces(token)
                       UNION ALL
                       SELECT lower(btrim(token, '_$')) AS term, 2 AS priority
                       FROM regexp_split_to_table(
                           regexp_replace(
                               regexp_replace(p.content, '([a-z0-9])([A-Z])', '\1 \2', 'g'),
                               '([A-Z]+)([A-Z][a-z])', '\1 \2', 'g'
                           ),
                           '[^A-Za-z0-9$]+'
                       ) AS pieces(token)
                   ) terms
                   WHERE length(term) >= 2
                   GROUP BY term
                   ORDER BY min(priority), term
                   LIMIT 256
               ),
               'source-code-search-terms-v1'
        FROM projected p
        ON CONFLICT (chunk_id) DO UPDATE SET
            path=EXCLUDED.path,
            full_identifiers=EXCLUDED.full_identifiers,
            normalized_terms=EXCLUDED.normalized_terms,
            search_version=EXCLUDED.search_version;

        GET DIAGNOSTICS inserted_rows = ROW_COUNT;
        EXIT WHEN inserted_rows = 0;
    END LOOP;
END $$;
