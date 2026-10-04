-- Evidence IDs are local to the source that produced them. Keep each exact
-- immutable raw owner even when another source uses the same local ID.
DROP INDEX IF EXISTS source_wiki_current_evidence;
DROP INDEX IF EXISTS source_wiki_revision_evidence;
CREATE UNIQUE INDEX source_wiki_current_evidence
    ON source_wiki_evidence_refs(page_id,version,evidence_id,source_file_id,file_version_id,snapshot_id)
    WHERE revision_id IS NULL;
CREATE UNIQUE INDEX source_wiki_revision_evidence
    ON source_wiki_evidence_refs(revision_id,evidence_id,source_file_id,file_version_id,snapshot_id)
    WHERE revision_id IS NOT NULL;

-- Backfill owners for already-persisted typed contributions. Invalid or
-- incomplete provenance is deliberately omitted and remains unreadable.
INSERT INTO source_wiki_evidence_refs (
    id,page_id,revision_id,version,evidence_id,source_file_id,file_version_id,snapshot_id,path,commit_sha
)
SELECT gen_random_uuid()::text,c.page_id,c.revision_id,c.page_version,
       evidence.item->>'id',evidence.item->>'knowledge_id',evidence.item->>'file_version_id',
       evidence.item->>'snapshot_id',evidence.item->>'path',evidence.item->>'commit_sha'
FROM source_wiki_page_contributions c
JOIN wiki_pages wp ON wp.id=c.page_id
LEFT JOIN wiki_page_revisions rev ON rev.id=c.revision_id AND rev.page_id=c.page_id AND rev.version=c.page_version
CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(c.contribution->'source_provenance'->'evidence')='array'
         THEN c.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END
) AS evidence(item)
JOIN source_files sf ON sf.id=evidence.item->>'knowledge_id'
JOIN source_snapshots ss ON ss.id=evidence.item->>'snapshot_id' AND ss.state='published'
JOIN source_file_versions sv ON sv.id=evidence.item->>'file_version_id'
    AND sv.source_file_id=sf.id AND sv.snapshot_id=ss.id
WHERE ((c.revision_id IS NULL AND wp.version=c.page_version) OR rev.id IS NOT NULL)
  AND c.state IN ('ready','stale')
  AND (SELECT count(*) FROM source_wiki_page_contributions cc
       WHERE cc.page_id=c.page_id AND cc.revision_id IS NOT DISTINCT FROM c.revision_id
         AND cc.page_version=c.page_version) BETWEEN 1 AND 128
  AND jsonb_typeof(c.contribution->'source_provenance'->'evidence')='array'
  AND jsonb_array_length(c.contribution->'source_provenance'->'evidence') BETWEEN 1 AND 256
  AND c.contribution->>'has_unattributed_body'='false'
  AND c.contribution->>'topic_kind'=c.topic_kind
  AND c.contribution->>'topic_key'=c.topic_key
  AND c.contribution->'source_provenance'->>'source_id'=c.source_id
  AND c.contribution->'source_provenance'->>'topic_kind'=c.topic_kind
  AND c.contribution->'source_provenance'->>'topic_key'=c.topic_key
  AND c.contribution->'source_provenance'->>'applicable_snapshot_id'=c.applicable_snapshot_id
  AND c.contribution->'source_provenance'->>'state' IN ('ready','stale')
  AND (c.state='stale' OR (c.target_snapshot_id=c.applicable_snapshot_id AND c.contribution->'source_provenance'->>'state'='ready'))
  AND sf.tenant_id=wp.tenant_id AND sf.knowledge_base_id=wp.knowledge_base_id
  AND sf.data_source_id=c.source_id AND evidence.item->>'data_source_id'=c.source_id
  AND evidence.item->>'id' IS NOT NULL AND evidence.item->>'path' IS NOT NULL
  AND EXISTS(SELECT 1 FROM jsonb_array_elements_text(
                 CASE WHEN c.revision_id IS NULL THEN
                      CASE WHEN jsonb_typeof(wp.source_refs::jsonb)='array' THEN wp.source_refs::jsonb ELSE '[]'::jsonb END
                      ELSE CASE WHEN jsonb_typeof(rev.source_refs::jsonb)='array' THEN rev.source_refs::jsonb ELSE '[]'::jsonb END
                 END
             ) wr
             WHERE split_part(wr,'|',1)=evidence.item->>'knowledge_id')
  AND evidence.item->>'commit_sha'=ss.commit_sha
  AND evidence.item->>'sha256'=sv.sha256
  AND evidence.item->>'text_sha256' IS NOT NULL
  AND CASE
      WHEN (evidence.item->'range'->>'start_byte') ~ '^[0-9]+$'
       AND (evidence.item->'range'->>'end_byte') ~ '^[0-9]+$'
      THEN CASE
          WHEN (evidence.item->'range'->>'start_byte')::numeric>=0
           AND (evidence.item->'range'->>'end_byte')::numeric>(evidence.item->'range'->>'start_byte')::numeric
           AND (evidence.item->'range'->>'end_byte')::numeric<=octet_length(sv.content)
          THEN encode(sha256(substring(sv.content FROM (evidence.item->'range'->>'start_byte')::int+1
               FOR (evidence.item->'range'->>'end_byte')::int-(evidence.item->'range'->>'start_byte')::int)),'hex')=evidence.item->>'text_sha256'
          ELSE FALSE
      END
      ELSE FALSE
  END
  AND encode(sha256(sv.content),'hex')=sv.sha256
ON CONFLICT DO NOTHING;

-- A lease may pin the same local ID from distinct source/file versions.
-- Partial indexes keep current and historical owners separately addressable.
ALTER TABLE source_read_wiki_evidence_refs
    DROP CONSTRAINT IF EXISTS source_read_wiki_evidence_refs_pkey;
CREATE UNIQUE INDEX source_read_wiki_current_evidence
    ON source_read_wiki_evidence_refs(lease_id,page_id,version,evidence_id,source_file_id,file_version_id,snapshot_id)
    WHERE revision_id IS NULL;
CREATE UNIQUE INDEX source_read_wiki_revision_evidence
    ON source_read_wiki_evidence_refs(lease_id,page_id,revision_id,version,evidence_id,source_file_id,file_version_id,snapshot_id)
    WHERE revision_id IS NOT NULL;
