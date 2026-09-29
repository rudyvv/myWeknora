package source

import (
	"context"
)

type wikiAnswerKey struct{}

// WithWikiAnswerRead excludes unverified and stale technical prose from answers.
func WithWikiAnswerRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, wikiAnswerKey{}, true)
}
func IsWikiAnswerRead(ctx context.Context) bool { v, _ := ctx.Value(wikiAnswerKey{}).(bool); return v }

// SourcePermissionSQL applies repository/file/tag permissions independent of a
// historical evidence snapshot. Only a server-pinned question supplies grants.
func SourcePermissionSQL(ctx context.Context, sourceColumn, fileColumn string) string {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	if !ok {
		return "FALSE"
	}
	return `EXISTS(SELECT 1 FROM source_read_wiki_scopes rs JOIN source_read_leases rl ON rl.id=rs.lease_id
 JOIN data_sources wd ON wd.id=` + sourceColumn + ` AND wd.tenant_id=rs.tenant_id AND wd.knowledge_base_id=rs.knowledge_base_id
 JOIN source_publications wp ON wp.data_source_id=wd.id AND wp.tenant_id=wd.tenant_id AND wp.knowledge_base_id=wd.knowledge_base_id
 WHERE rl.id='` + scope.leaseID + `' AND rl.expires_at>now() AND wd.deleted_at IS NULL AND wd.config->'settings'->>'content_mode'='source'
 AND (jsonb_array_length(rs.source_ids)=0 OR jsonb_exists(rs.source_ids,wd.id))
 AND (jsonb_array_length(rs.knowledge_ids)=0 OR jsonb_exists(rs.knowledge_ids,` + fileColumn + `))
 AND (jsonb_array_length(rs.tag_ids)=0 OR EXISTS(SELECT 1 FROM knowledge_tag_relations wt WHERE wt.knowledge_id=` + fileColumn + ` AND jsonb_exists(rs.tag_ids,wt.tag_id))))`
}
