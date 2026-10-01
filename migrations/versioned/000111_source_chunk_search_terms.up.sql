-- Source-only code terms are a separately indexed projection. They never
-- rewrite chunks, evidence, or the T12-budgeted embedding text.
CREATE TABLE IF NOT EXISTS source_chunk_search_terms (
    chunk_id VARCHAR(36) PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    full_identifiers TEXT[] NOT NULL DEFAULT '{}',
    normalized_terms TEXT[] NOT NULL DEFAULT '{}',
    search_version TEXT NOT NULL
);

-- Staging and historical backfill must use one bounded projection definition.
-- The chunk reference is the lookup key so StageFile need not resend potentially
-- large parser facts/symbol arrays for every chunk.
CREATE OR REPLACE FUNCTION source_chunk_search_projection_v1(p_chunk_id CHARACTER VARYING)
RETURNS TABLE (
    path TEXT,
    full_identifiers TEXT[],
    normalized_terms TEXT[],
    search_version TEXT
)
LANGUAGE SQL
STABLE
AS $source_search_projection$
WITH source_input AS MATERIALIZED (
    SELECT c.id AS chunk_id,
           sm.path,
           c.content,
           c.metadata::jsonb AS metadata,
           COALESCE(sv.facts, '[]'::jsonb) AS facts,
           COALESCE(sv.symbols, '[]'::jsonb) AS symbols
    FROM chunks c
    JOIN source_chunk_references sc ON sc.chunk_id=c.id
    JOIN source_snapshot_members sm
      ON sm.snapshot_id=sc.snapshot_id
     AND sm.source_file_id=sc.source_file_id
     AND sm.file_version_id=sc.file_version_id
     AND sm.status='parsed'
    JOIN source_file_versions sv
      ON sv.id=sc.file_version_id
     AND sv.source_file_id=sc.source_file_id
     AND sv.snapshot_id=sc.snapshot_id
    WHERE c.id=p_chunk_id
), facts_in_chunk AS MATERIALIZED (
    SELECT i.chunk_id, fact.value AS fact, fact.ordinality
    FROM source_input i
    CROSS JOIN LATERAL jsonb_array_elements(i.facts)
        WITH ORDINALITY AS fact(value, ordinality)
    WHERE COALESCE((fact.value->'range'->>'start_byte')::INTEGER, -1)
              < COALESCE((i.metadata #>> '{source,range,end_byte}')::INTEGER, -1)
      AND COALESCE((fact.value->'range'->>'end_byte')::INTEGER, -1)
              > COALESCE((i.metadata #>> '{source,range,start_byte}')::INTEGER, 2147483647)
), symbols_in_chunk AS MATERIALIZED (
    SELECT i.chunk_id, symbol.value AS symbol, symbol.ordinality
    FROM source_input i
    CROSS JOIN LATERAL jsonb_array_elements(i.symbols)
        WITH ORDINALITY AS symbol(value, ordinality)
    WHERE COALESCE((symbol.value->'range'->>'start_byte')::INTEGER, -1)
              < COALESCE((i.metadata #>> '{source,range,end_byte}')::INTEGER, -1)
      AND COALESCE((symbol.value->'range'->>'end_byte')::INTEGER, -1)
              > COALESCE((i.metadata #>> '{source,range,start_byte}')::INTEGER, 2147483647)
), identifier_values AS (
    SELECT i.chunk_id, i.path, i.path AS value,
           0 AS category, 0::BIGINT AS source_ordinality, 0 AS value_ordinality
    FROM source_input i
    UNION ALL
    SELECT i.chunk_id, i.path, s.symbol->>'qualified_name',
           2, s.ordinality, 0
    FROM source_input i
    JOIN symbols_in_chunk s ON s.chunk_id=i.chunk_id
    UNION ALL
    SELECT i.chunk_id, i.path, s.symbol->>'name',
           2, s.ordinality, 1
    FROM source_input i
    JOIN symbols_in_chunk s ON s.chunk_id=i.chunk_id
    UNION ALL
    SELECT i.chunk_id, i.path, v.value,
           1, f.ordinality, v.ordinality
    FROM source_input i
    JOIN facts_in_chunk f ON f.chunk_id=i.chunk_id
    CROSS JOIN LATERAL (
        VALUES
            (1, CASE f.fact->>'kind'
                    WHEN 'mybatis_statement'
                        THEN COALESCE(NULLIF(f.fact->>'statement_id', ''), f.fact->>'name')
                    WHEN 'mybatis_result_map' THEN f.fact->>'name'
                    WHEN 'mybatis_sql_fragment' THEN f.fact->>'name'
                    WHEN 'java_mapper_method' THEN f.fact->>'name'
                END),
            (2, CASE f.fact->>'kind'
                    WHEN 'java_mapper_method'
                        THEN NULLIF(f.fact->>'namespace', '')
                    WHEN 'mybatis_statement'
                        THEN NULLIF(f.fact->>'namespace', '') || '#' ||
                             COALESCE(NULLIF(f.fact->>'statement_id', ''), NULLIF(f.fact->>'name', ''))
                    WHEN 'mybatis_result_map'
                        THEN NULLIF(f.fact->>'namespace', '') || '#' || NULLIF(f.fact->>'name', '')
                    WHEN 'mybatis_sql_fragment'
                        THEN NULLIF(f.fact->>'namespace', '') || '#' || NULLIF(f.fact->>'name', '')
                    WHEN 'java_mapper_method'
                        THEN NULLIF(f.fact->>'namespace', '') || '#' || NULLIF(f.fact->>'name', '')
                END),
            (3, CASE WHEN f.fact->>'kind'='java_mapper_method'
                     THEN NULLIF(f.fact->>'namespace', '') || '#' || NULLIF(f.fact->>'name', '') END)
    ) AS v(ordinality, value)
    WHERE v.value IS NOT NULL
      AND f.fact->>'kind' IN (
          'mybatis_statement','mybatis_result_map','mybatis_sql_fragment','java_mapper_method'
      )
    UNION ALL
    SELECT i.chunk_id, i.path, NULLIF(f.fact->>'namespace', ''),
           1, f.ordinality, 1
    FROM source_input i
    JOIN facts_in_chunk f ON f.chunk_id=i.chunk_id
    WHERE f.fact->>'kind'='mybatis_mapper'
    UNION ALL
    SELECT i.chunk_id, i.path, symbol.value,
           3, symbol.ordinality, 0
    FROM source_input i
    CROSS JOIN LATERAL jsonb_array_elements_text(
        COALESCE(i.metadata #> '{source,symbols}', '[]'::jsonb)
    ) WITH ORDINALITY AS symbol(value, ordinality)
), identifier_candidates AS (
    SELECT chunk_id, path, btrim(value) AS value,
           category, source_ordinality, value_ordinality
    FROM identifier_values
    WHERE value IS NOT NULL
      AND btrim(value) <> ''
      AND octet_length(btrim(value)) <= 512
), unique_identifiers AS (
    SELECT DISTINCT ON (chunk_id, value)
           chunk_id, path, value, category, source_ordinality, value_ordinality
    FROM identifier_candidates
    ORDER BY chunk_id, value, category, source_ordinality, value_ordinality
), bounded_identifiers AS (
    SELECT chunk_id, path, value,
           row_number() OVER (
               PARTITION BY chunk_id
               ORDER BY category, source_ordinality, value_ordinality
           ) AS identifier_ordinality
    FROM unique_identifiers
), source_projection AS MATERIALIZED (
    SELECT i.chunk_id,
           i.path,
           i.content,
           ARRAY(
               SELECT candidate.value
               FROM bounded_identifiers candidate
               WHERE candidate.chunk_id=i.chunk_id
               ORDER BY candidate.identifier_ordinality
               LIMIT 256
           ) AS full_identifiers
    FROM source_input i
), identifier_parts AS (
    SELECT p.chunk_id,
           identifier.ordinality AS identifier_ordinality,
           token.ordinality AS token_ordinality,
           token.match_value[1] AS token
    FROM source_projection p
    CROSS JOIN LATERAL unnest(p.full_identifiers)
        WITH ORDINALITY AS identifier(value, ordinality)
    CROSS JOIN LATERAL regexp_matches(
        identifier.value, '[A-Za-z_$][A-Za-z0-9_$]*', 'g'
    ) WITH ORDINALITY AS token(match_value, ordinality)
), body_parts AS (
    SELECT p.chunk_id,
           token.ordinality AS token_ordinality,
           token.match_value[1] AS token
    FROM source_projection p
    CROSS JOIN LATERAL regexp_matches(
        p.content, '[A-Za-z_$][A-Za-z0-9_$]*', 'g'
    ) WITH ORDINALITY AS token(match_value, ordinality)
    WHERE token.ordinality <= 256
), identifier_term_candidates AS (
    SELECT p.chunk_id, p.identifier_ordinality, p.token_ordinality,
           0 AS form_ordinality, 0::BIGINT AS part_ordinality,
           lower(btrim(p.token, '_$')) AS term
    FROM identifier_parts p
    UNION ALL
    SELECT p.chunk_id, p.identifier_ordinality, p.token_ordinality,
           1, split.ordinality, lower(btrim(split.match_value[1], '_$'))
    FROM identifier_parts p
    CROSS JOIN LATERAL regexp_matches(
        replace(
            regexp_replace(
                regexp_replace(p.token, '([A-Z]+)([A-Z][a-z])', '\1 \2', 'g'),
                '([a-z0-9])([A-Z])', '\1 \2', 'g'
            ),
            '_', ' '
        ),
        '[A-Za-z_$][A-Za-z0-9_$]*', 'g'
    ) WITH ORDINALITY AS split(match_value, ordinality)
), unique_identifier_terms AS (
    SELECT DISTINCT ON (chunk_id, identifier_ordinality, term)
           chunk_id, identifier_ordinality, token_ordinality,
           form_ordinality, part_ordinality, term
    FROM identifier_term_candidates
    WHERE char_length(term) >= 2
    ORDER BY chunk_id, identifier_ordinality, term,
             token_ordinality, form_ordinality, part_ordinality
), bounded_identifier_terms AS (
    SELECT chunk_id, identifier_ordinality, token_ordinality,
           form_ordinality, part_ordinality, term,
           row_number() OVER (
               PARTITION BY chunk_id, identifier_ordinality
               ORDER BY token_ordinality, form_ordinality, part_ordinality
           ) AS term_ordinality
    FROM unique_identifier_terms
), body_term_candidates AS (
    SELECT p.chunk_id, p.token_ordinality, 0 AS form_ordinality,
           0::BIGINT AS part_ordinality,
           lower(btrim(p.token, '_$')) AS term
    FROM body_parts p
    UNION ALL
    SELECT p.chunk_id, p.token_ordinality, 1, split.ordinality,
           lower(btrim(split.match_value[1], '_$'))
    FROM body_parts p
    CROSS JOIN LATERAL regexp_matches(
        replace(
            regexp_replace(
                regexp_replace(p.token, '([A-Z]+)([A-Z][a-z])', '\1 \2', 'g'),
                '([a-z0-9])([A-Z])', '\1 \2', 'g'
            ),
            '_', ' '
        ),
        '[A-Za-z_$][A-Za-z0-9_$]*', 'g'
    ) WITH ORDINALITY AS split(match_value, ordinality)
), unique_body_terms AS (
    SELECT DISTINCT ON (chunk_id, token_ordinality, term)
           chunk_id, token_ordinality, form_ordinality, part_ordinality, term
    FROM body_term_candidates
    WHERE char_length(term) >= 2
    ORDER BY chunk_id, token_ordinality, term, form_ordinality, part_ordinality
), bounded_body_terms AS (
    SELECT chunk_id, token_ordinality, form_ordinality, part_ordinality, term,
           row_number() OVER (
               PARTITION BY chunk_id, token_ordinality
               ORDER BY form_ordinality, part_ordinality
           ) AS term_ordinality
    FROM unique_body_terms
), normalized_candidates AS (
    SELECT chunk_id,
           0 AS lane,
           identifier_ordinality AS source_ordinality,
           token_ordinality,
           form_ordinality,
           part_ordinality,
           term
    FROM bounded_identifier_terms
    WHERE term_ordinality <= 64
    UNION ALL
    SELECT chunk_id, 1, 0::BIGINT, token_ordinality,
           form_ordinality, part_ordinality, term
    FROM bounded_body_terms
    WHERE term_ordinality <= 64
), unique_terms AS (
    SELECT DISTINCT ON (chunk_id, term)
           chunk_id, term, lane, source_ordinality, token_ordinality,
           form_ordinality, part_ordinality
    FROM normalized_candidates
    WHERE char_length(term) >= 2
    ORDER BY chunk_id, term, lane, source_ordinality, token_ordinality,
             form_ordinality, part_ordinality
)
SELECT p.path,
       p.full_identifiers,
       ARRAY(
           SELECT term.term
           FROM unique_terms term
           WHERE term.chunk_id=p.chunk_id
           ORDER BY term.lane, term.source_ordinality, term.token_ordinality,
                    term.form_ordinality, term.part_ordinality
           LIMIT 256
       ) AS normalized_terms,
       'source-code-search-terms-v1'::TEXT AS search_version
FROM source_projection p;
$source_search_projection$;
COMMENT ON FUNCTION source_chunk_search_projection_v1(character varying)
    IS 'source-code-search-terms-v1';

CREATE INDEX IF NOT EXISTS source_chunk_search_terms_full_gin
    ON source_chunk_search_terms USING GIN (full_identifiers);
CREATE INDEX IF NOT EXISTS source_chunk_search_terms_terms_gin
    ON source_chunk_search_terms USING GIN (normalized_terms);

-- Upgrade existing snapshots in bounded, repeatable batches. The same function
-- is called by StageFile for new chunks, so both producers persist identical
-- arrays without reading embeddings.content or invoking an embedding model.
DO $$
DECLARE
    inserted_rows INTEGER;
BEGIN
    LOOP
        WITH batch AS MATERIALIZED (
            SELECT sc.chunk_id
            FROM source_chunk_references sc
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
                WHERE existing.chunk_id=sc.chunk_id
                  AND existing.search_version='source-code-search-terms-v1'
            )
            ORDER BY sc.chunk_id
            LIMIT 1000
        )
        INSERT INTO source_chunk_search_terms
            (chunk_id, path, full_identifiers, normalized_terms, search_version)
        SELECT batch.chunk_id, projection.path, projection.full_identifiers,
               projection.normalized_terms, projection.search_version
        FROM batch
        CROSS JOIN LATERAL source_chunk_search_projection_v1(batch.chunk_id) projection
        ON CONFLICT (chunk_id) DO UPDATE SET
            path=EXCLUDED.path,
            full_identifiers=EXCLUDED.full_identifiers,
            normalized_terms=EXCLUDED.normalized_terms,
            search_version=EXCLUDED.search_version;

        GET DIAGNOSTICS inserted_rows = ROW_COUNT;
        EXIT WHEN inserted_rows = 0;
    END LOOP;
END $$;
