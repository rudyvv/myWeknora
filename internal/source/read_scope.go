package source

import (
	"context"
	"fmt"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

type readScopeKey struct{}
type relationCursorKey struct{}
type relationPageSizeKey struct{}

// WithRelationCursor carries an opaque page token through the existing source
// file read path. Repository readers bind it to the authorized file version
// and snapshot before using it.
func WithRelationCursor(ctx context.Context, cursor string) context.Context {
	if cursor == "" {
		return ctx
	}
	return context.WithValue(ctx, relationCursorKey{}, cursor)
}

func RelationCursorFromContext(ctx context.Context) string {
	cursor, _ := ctx.Value(relationCursorKey{}).(string)
	return cursor
}

// WithoutRelationCursor preserves the read lease and all other source scope
// while clearing a file-bound relation cursor before a cross-file endpoint
// read.
func WithoutRelationCursor(ctx context.Context) context.Context {
	return context.WithValue(ctx, relationCursorKey{}, "")
}

// WithRelationPageSize may lower (never raise) the repository's bounded
// default for consumers such as Agent output.
func WithRelationPageSize(ctx context.Context, pageSize int) context.Context {
	if pageSize < 1 || pageSize > 100 {
		return ctx
	}
	return context.WithValue(ctx, relationPageSizeKey{}, pageSize)
}

func RelationPageSizeFromContext(ctx context.Context, defaultPageSize int) int {
	pageSize, _ := ctx.Value(relationPageSizeKey{}).(int)
	if pageSize < 1 || pageSize > defaultPageSize {
		return defaultPageSize
	}
	return pageSize
}

type readScope struct {
	leaseID    string
	hasSources bool
	caller     types.Caller
	validate   func(context.Context) error
	mu         sync.Mutex
	readers    int
	release    func()
}

// WithReadScope carries only a server-created lease, bound to its caller.
// The permission callback performs a fresh check on every public read.
func WithReadScope(ctx context.Context, lease types.SourceReadLease, validate func(context.Context) error, release func()) (context.Context, func()) {
	if _, err := uuid.Parse(lease.ID); err != nil {
		panic("source read lease must be a server-generated UUID")
	}
	scope := &readScope{leaseID: lease.ID, hasSources: lease.HasSources, caller: types.CallerFromContext(ctx), validate: validate, readers: 1, release: release}
	return context.WithValue(types.WithCaller(ctx, scope.caller), readScopeKey{}, scope), scope.releaseReader()
}

func (s *readScope) releaseReader() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.readers--
			last := s.readers == 0
			s.mu.Unlock()
			if last {
				s.release()
			}
		})
	}
}

// RetainReadScope lets an asynchronous answer stream finish after the pipeline
// returns. The final reader releases the durable lease; cancellation still wins.
func RetainReadScope(ctx context.Context) (func(), error) {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	if !ok {
		return func() {}, nil
	}
	scope.mu.Lock()
	if scope.readers == 0 {
		scope.mu.Unlock()
		return nil, fmt.Errorf("source question already completed")
	}
	scope.readers++
	scope.mu.Unlock()
	return scope.releaseReader(), nil
}

func HasReadScope(ctx context.Context) bool {
	_, ok := ctx.Value(readScopeKey{}).(*readScope)
	return ok
}

// ReadLeaseID returns the server-created durable lease carried by ctx. It is
// intended for repository code that must attach an exact resource pin to the
// same lease that performs the authorized read.
func ReadLeaseID(ctx context.Context) (string, bool) {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	if !ok || scope.leaseID == "" {
		return "", false
	}
	return scope.leaseID, true
}

func HasPinnedSources(ctx context.Context) bool {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	return ok && scope.hasSources
}

// InheritReadScope preserves the execution context's cancellation and caller.
func InheritReadScope(ctx, pinned context.Context) context.Context {
	if scope, ok := pinned.Value(readScopeKey{}).(*readScope); ok {
		return context.WithValue(ctx, readScopeKey{}, scope)
	}
	return ctx
}

func ValidateReadScope(ctx context.Context) error {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	if !ok {
		return nil
	}
	if types.CallerFromContext(ctx) != scope.caller {
		return fmt.Errorf("source question belongs to another caller")
	}
	if scope.validate != nil {
		return scope.validate(ctx)
	}
	return nil
}

// SnapshotSQL is shared by indexes, chunk reads and complete-file reads.
// All expressions are trusted repository columns. The sole literal is a
// validated server-generated UUID; user scopes are stored as bound JSONB.
func SnapshotSQL(ctx context.Context, snapshotColumn, sourceColumn, fileColumn string) string {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	if !ok {
		return `EXISTS (SELECT 1 FROM source_publications sp
		JOIN data_sources lifecycle_ds ON lifecycle_ds.id=sp.data_source_id
		WHERE sp.snapshot_id=` + snapshotColumn + ` AND sp.data_source_id=` + sourceColumn + `
		AND lifecycle_ds.source_query_enabled IS TRUE AND lifecycle_ds.deleted_at IS NULL)`
	}
	return `EXISTS (SELECT 1 FROM source_read_scopes rs JOIN source_read_leases rl ON rl.id=rs.lease_id
		JOIN data_sources lifecycle_ds ON lifecycle_ds.id=rs.data_source_id
		WHERE rl.id='` + scope.leaseID + `' AND rl.expires_at>now() AND rs.snapshot_id=` + snapshotColumn + ` AND rs.data_source_id=` + sourceColumn + `
		AND lifecycle_ds.source_query_enabled IS TRUE AND lifecycle_ds.deleted_at IS NULL
		AND (jsonb_array_length(rs.knowledge_ids)=0 OR jsonb_exists(rs.knowledge_ids, ` + fileColumn + `))
		AND (jsonb_array_length(rs.tag_ids)=0 OR EXISTS (SELECT 1 FROM knowledge_tag_relations ktr
		 WHERE ktr.knowledge_id=` + fileColumn + ` AND jsonb_exists(rs.tag_ids, ktr.tag_id))))`
}

// OrdinaryKnowledgeSQL retains ordinary-document behavior within the selected
// targets, including mixed questions. Repository-only targets grant no documents.
func OrdinaryKnowledgeSQL(ctx context.Context, column string) string {
	return ordinaryScopedSQL(ctx, column, "")
}

func OrdinaryChunkSQL(ctx context.Context, chunkColumn, knowledgeColumn string) string {
	return ordinaryScopedSQL(ctx, knowledgeColumn, chunkColumn)
}

func ordinaryScopedSQL(ctx context.Context, column, chunkColumn string) string {
	scope, ok := ctx.Value(readScopeKey{}).(*readScope)
	if !ok {
		return "TRUE"
	}
	faqChunk := ""
	if chunkColumn != "" {
		faqChunk = " AND ft.id=" + chunkColumn
	}
	return `EXISTS(SELECT 1 FROM source_read_document_scopes dr JOIN source_read_leases rl ON rl.id=dr.lease_id
	JOIN knowledges dk ON dk.id=` + column + ` AND dk.knowledge_base_id=dr.knowledge_base_id AND dk.tenant_id=dr.tenant_id
	WHERE rl.id='` + scope.leaseID + `' AND rl.expires_at>now()
	AND (jsonb_array_length(dr.knowledge_ids)=0 OR jsonb_exists(dr.knowledge_ids,dk.id))
	AND (jsonb_array_length(dr.tag_ids)=0
	 OR (dk.type='faq' AND EXISTS(SELECT 1 FROM chunks ft WHERE ft.knowledge_id=dk.id AND ft.tenant_id=dk.tenant_id AND ft.knowledge_base_id=dk.knowledge_base_id AND ft.chunk_type='faq' AND ft.deleted_at IS NULL AND ft.is_enabled=true AND jsonb_exists(dr.tag_ids,ft.tag_id)` + faqChunk + `))
	 OR (dk.type IS DISTINCT FROM 'faq' AND EXISTS(SELECT 1 FROM knowledge_tag_relations dtr WHERE dtr.knowledge_id=dk.id AND jsonb_exists(dr.tag_ids,dtr.tag_id)))))`
}
