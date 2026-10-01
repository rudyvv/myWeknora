package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pgRepository implements PostgreSQL-based retrieval operations
type pgRepository struct {
	db               *gorm.DB // Database connection
	sourceVisibility bool
}

// Source tags belong to stable files and can change without rebuilding their
// immutable indexes. Both retrieval routes apply this relation before LIMIT;
// existing FAQ/index tag filtering remains available. marks contains only SQL
// placeholders produced by the repository, never request text.
func sourceTagFilterSQL(marks string) string {
	return `(embeddings.tag_id IN (` + marks + `) OR EXISTS (
		SELECT 1 FROM source_files sf
		JOIN knowledge_tag_relations ktr ON ktr.knowledge_id=sf.id
		JOIN knowledge_tags kt ON kt.id=ktr.tag_id AND kt.tenant_id=sf.tenant_id AND kt.knowledge_base_id=sf.knowledge_base_id
		WHERE sf.id=embeddings.knowledge_id AND sf.knowledge_base_id=embeddings.knowledge_base_id
		AND ktr.tag_id IN (` + marks + `)))`
}

func sourceRepositoryFilterSQL(marks string) string {
	return `EXISTS (SELECT 1 FROM source_chunk_references sc
		JOIN source_files sf ON sf.id=sc.source_file_id
		WHERE sc.chunk_id=embeddings.chunk_id AND sf.knowledge_base_id=embeddings.knowledge_base_id
		AND sf.data_source_id IN (` + marks + `))`
}

// NewPostgresRetrieveEngineRepository creates a new PostgreSQL retriever repository
func NewPostgresRetrieveEngineRepository(db *gorm.DB) interfaces.RetrieveEngineRepository {
	logger.GetLogger(context.Background()).Info("[Postgres] Initializing PostgreSQL retriever engine repository")
	return &pgRepository{db: db}
}

// Bound PG stores retain their document-only schema; the built-in DB has the
// complete source manifest and publication pointer used by this provider.
func NewSourceAwarePostgresRetrieveEngineRepository(db *gorm.DB) interfaces.RetrieveEngineRepository {
	return &pgRepository{db: db, sourceVisibility: true}
}

// EngineType returns the retriever engine type (PostgreSQL)
func (r *pgRepository) EngineType() types.RetrieverEngineType {
	return types.PostgresRetrieverEngineType
}

// Support returns supported retriever types (keywords and vector)
func (r *pgRepository) Support() []types.RetrieverType {
	return []types.RetrieverType{types.KeywordsRetrieverType, types.VectorRetrieverType}
}

// CheckSourceIndexes verifies both extensions on this engine's actual database.
func (r *pgRepository) CheckSourceIndexes(ctx context.Context) error {
	var extensions struct {
		Vector bool
		BM25   bool
	}
	if err := r.db.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') AS vector, EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_search') AS bm25").Scan(&extensions).Error; err != nil {
		return err
	}
	if !extensions.Vector || !extensions.BM25 {
		return fmt.Errorf("source indexes require vector and pg_search extensions")
	}
	return nil
}

// calculateIndexStorageSize calculates storage size for a single index entry
func (g *pgRepository) calculateIndexStorageSize(embeddingDB *pgVector) int64 {
	// 1. Text content size
	contentSizeBytes := int64(len(embeddingDB.Content))

	// 2. Vector storage size (2 bytes per dimension for half-precision float)
	var vectorSizeBytes int64 = 0
	if embeddingDB.Dimension > 0 {
		vectorSizeBytes = int64(embeddingDB.Dimension * 2)
	}

	// 3. Metadata size (fixed overhead for IDs, timestamps etc.)
	metadataSizeBytes := int64(200)

	// 4. Index overhead (HNSW index is ~2x vector size)
	indexOverheadBytes := vectorSizeBytes * 2

	// Total size in bytes
	totalSizeBytes := contentSizeBytes + vectorSizeBytes + metadataSizeBytes + indexOverheadBytes

	return totalSizeBytes
}

// EstimateStorageSize estimates total storage size for multiple indices
func (g *pgRepository) EstimateStorageSize(
	ctx context.Context, indexInfoList []*types.IndexInfo, additionalParams map[string]any,
) int64 {
	var totalStorageSize int64 = 0
	for _, indexInfo := range indexInfoList {
		embeddingDB := toDBVectorEmbedding(indexInfo, additionalParams)
		totalStorageSize += g.calculateIndexStorageSize(embeddingDB)
	}
	logger.GetLogger(ctx).Infof(
		"[Postgres] Estimated storage size for %d indices: %d bytes",
		len(indexInfoList), totalStorageSize,
	)
	return totalStorageSize
}

// Save stores a single index entry
func (g *pgRepository) Save(ctx context.Context, indexInfo *types.IndexInfo, additionalParams map[string]any) error {
	logger.GetLogger(ctx).Debugf("[Postgres] Saving index for source ID: %s", indexInfo.SourceID)
	embeddingDB := toDBVectorEmbedding(indexInfo, additionalParams)
	err := g.db.WithContext(ctx).Create(embeddingDB).Error
	if err != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Failed to save index: %v", err)
		return err
	}
	logger.GetLogger(ctx).Infof("[Postgres] Successfully saved index for source ID: %s", indexInfo.SourceID)
	return nil
}

// BatchSave stores multiple index entries in batch
func (g *pgRepository) BatchSave(
	ctx context.Context, indexInfoList []*types.IndexInfo, additionalParams map[string]any,
) error {
	logger.GetLogger(ctx).Infof("[Postgres] Batch saving %d indices", len(indexInfoList))
	indexInfoDBList := make([]*pgVector, len(indexInfoList))
	for i := range indexInfoList {
		indexInfoDBList[i] = toDBVectorEmbedding(indexInfoList[i], additionalParams)
	}
	err := g.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(indexInfoDBList).Error
	if err != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Batch save failed: %v", err)
		return err
	}
	logger.GetLogger(ctx).Infof("[Postgres] Successfully batch saved %d indices", len(indexInfoList))
	return nil
}

// DeleteByChunkIDList deletes indices by chunk IDs
func (g *pgRepository) DeleteByChunkIDList(ctx context.Context, chunkIDList []string, dimension int, knowledgeType string) error {
	logger.GetLogger(ctx).Infof("[Postgres] Deleting indices by chunk IDs, count: %d", len(chunkIDList))
	result := g.db.WithContext(ctx).Where("chunk_id IN ?", chunkIDList).Delete(&pgVector{})
	if result.Error != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Failed to delete indices by chunk IDs: %v", result.Error)
		return result.Error
	}
	logger.GetLogger(ctx).Infof("[Postgres] Successfully deleted %d indices by chunk IDs", result.RowsAffected)
	return nil
}

// DeleteBySourceIDList deletes indices by source IDs
func (g *pgRepository) DeleteBySourceIDList(ctx context.Context, sourceIDList []string, dimension int, knowledgeType string) error {
	if len(sourceIDList) == 0 {
		return nil
	}
	logger.GetLogger(ctx).Infof("[Postgres] Deleting indices by source IDs, count: %d", len(sourceIDList))
	result := g.db.WithContext(ctx).Where("source_id IN ?", sourceIDList).Delete(&pgVector{})
	if result.Error != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Failed to delete indices by source IDs: %v", result.Error)
		return result.Error
	}
	logger.GetLogger(ctx).Infof("[Postgres] Successfully deleted %d indices by source IDs", result.RowsAffected)
	return nil
}

// DeleteByKnowledgeIDList deletes indices by knowledge IDs
func (g *pgRepository) DeleteByKnowledgeIDList(ctx context.Context, knowledgeIDList []string, dimension int, knowledgeType string) error {
	logger.GetLogger(ctx).Infof("[Postgres] Deleting indices by knowledge IDs, count: %d", len(knowledgeIDList))
	result := g.db.WithContext(ctx).Where("knowledge_id IN ?", knowledgeIDList).Delete(&pgVector{})
	if result.Error != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Failed to delete indices by knowledge IDs: %v", result.Error)
		return result.Error
	}
	logger.GetLogger(ctx).Infof("[Postgres] Successfully deleted %d indices by knowledge IDs", result.RowsAffected)
	return nil
}

// Retrieve handles retrieval requests and routes to appropriate method
func (g *pgRepository) Retrieve(ctx context.Context, params types.RetrieveParams) ([]*types.RetrieveResult, error) {
	logger.GetLogger(ctx).Debugf("[Postgres] Processing retrieval request of type: %s", params.RetrieverType)
	switch params.RetrieverType {
	case types.KeywordsRetrieverType:
		return g.KeywordsRetrieve(ctx, params)
	case types.VectorRetrieverType:
		return g.VectorRetrieve(ctx, params)
	}
	err := errors.New("invalid retriever type")
	logger.GetLogger(ctx).Errorf("[Postgres] %v: %s", err, params.RetrieverType)
	return nil, err
}

// KeywordsRetrieve performs keyword-based search using PostgreSQL full-text search
func (g *pgRepository) KeywordsRetrieve(ctx context.Context,
	params types.RetrieveParams,
) ([]*types.RetrieveResult, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	logger.GetLogger(ctx).Infof("[Postgres] Keywords retrieval: query=%s, topK=%d", params.Query, params.TopK)
	filters := make([]clause.Expression, 0)
	if g.sourceVisibility {
		filters = append(filters, clause.Expr{SQL: source.PublishedChunkSQL(ctx, "embeddings.chunk_id", "embeddings.knowledge_id")})
	}
	if len(params.SourceIDs) > 0 {
		if !g.sourceVisibility {
			return nil, errors.New("repository source scope requires the built-in source index")
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(params.SourceIDs)), ",")
		filters = append(filters, clause.Expr{SQL: sourceRepositoryFilterSQL(marks), Vars: common.ToInterfaceSlice(params.SourceIDs)})
	}

	// KnowledgeBaseIDs and KnowledgeIDs use AND logic
	// - If only KnowledgeBaseIDs: search entire knowledge bases
	// - If only KnowledgeIDs: search specific documents
	// - If both: search specific documents within the knowledge bases (AND)
	if len(params.KnowledgeBaseIDs) > 0 {
		logger.GetLogger(ctx).Debugf("[Postgres] Filtering by knowledge base IDs: %v", params.KnowledgeBaseIDs)
		filters = append(filters, clause.IN{
			Column: "knowledge_base_id",
			Values: common.ToInterfaceSlice(params.KnowledgeBaseIDs),
		})
	}
	if len(params.KnowledgeIDs) > 0 {
		logger.GetLogger(ctx).Debugf("[Postgres] Filtering by knowledge IDs: %v", params.KnowledgeIDs)
		filters = append(filters, clause.IN{
			Column: "knowledge_id",
			Values: common.ToInterfaceSlice(params.KnowledgeIDs),
		})
	}
	// Filter by tag IDs if specified
	if len(params.TagIDs) > 0 {
		logger.GetLogger(ctx).Debugf("[Postgres] Filtering by tag IDs: %v", params.TagIDs)
		values := common.ToInterfaceSlice(params.TagIDs)
		if g.sourceVisibility {
			marks := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
			filters = append(filters, clause.Expr{SQL: sourceTagFilterSQL(marks), Vars: append(append([]interface{}{}, values...), values...)})
		} else {
			filters = append(filters, clause.IN{Column: "tag_id", Values: values})
		}
	}
	filters = append(filters, clause.Expr{SQL: "(is_enabled IS NULL OR is_enabled = ?)", Vars: []interface{}{true}})

	// Use ParadeDB's ||| operator for matching any token
	bm25Conds := append([]clause.Expression(nil), filters...)
	bm25Conds = append(bm25Conds, clause.Expr{
		SQL:  "content ||| ?",
		Vars: []interface{}{params.Query},
	})
	bm25Conds = append(bm25Conds, clause.OrderBy{Columns: []clause.OrderByColumn{
		{Column: clause.Column{Name: "score"}, Desc: true},
	}})

	var bm25Rows []pgVectorWithScore
	err := g.db.WithContext(ctx).Clauses(bm25Conds...).Debug().
		Select([]string{
			"paradedb.score(id) as score",
			"id",
			"content",
			"source_id",
			"source_type",
			"chunk_id",
			"knowledge_id",
			"knowledge_base_id",
			"tag_id",
		}).
		Limit(int(params.TopK)).
		Find(&bm25Rows).Error

	if err == gorm.ErrRecordNotFound {
		logger.GetLogger(ctx).Warnf("[Postgres] No records found for keywords query: %s", params.Query)
		return nil, nil
	}
	if err != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Keywords retrieval failed: %v", err)
		return nil, err
	}

	codeQuery := source.SourceCodeQuery{}
	var exactRows, normalizedRows []pgVectorWithScore
	if g.sourceVisibility {
		codeQuery = source.ParseSourceCodeQuery(params.Query)
		if codeQuery.Enabled {
			exactRows, err = g.retrieveSourceCodeCandidates(ctx, filters, "full_identifiers", pq.Array(codeQuery.ExactIdentifiers), params.Query, params.TopK)
			if err != nil {
				return nil, err
			}
			normalizedRows, err = g.retrieveSourceCodeCandidates(ctx, filters, "normalized_terms", pq.Array(codeQuery.NormalizedTerms), params.Query, params.TopK)
			if err != nil {
				return nil, err
			}
		}
	}

	if len(exactRows)+len(normalizedRows) == 0 {
		results := make([]*types.IndexWithScore, len(bm25Rows))
		for i := range bm25Rows {
			results[i] = fromDBVectorEmbeddingWithScore(&bm25Rows[i], types.MatchTypeKeywords)
		}
		return keywordRetrieveResult(results), nil
	}

	type rankedCandidate struct {
		row  pgVectorWithScore
		tier int
	}
	candidates := make(map[string]rankedCandidate, len(exactRows)+len(normalizedRows)+len(bm25Rows))
	bm25Scores := make(map[string]float64, len(bm25Rows))
	for _, row := range bm25Rows {
		bm25Scores[row.ChunkID] = row.Score
		candidates[row.ChunkID] = rankedCandidate{row: row, tier: 2}
	}
	addCodeRows := func(rows []pgVectorWithScore, tier int) {
		for _, row := range rows {
			if score, ok := bm25Scores[row.ChunkID]; ok {
				row.Score = score
			}
			if current, ok := candidates[row.ChunkID]; ok && current.tier <= tier {
				continue
			}
			candidates[row.ChunkID] = rankedCandidate{row: row, tier: tier}
		}
	}
	addCodeRows(normalizedRows, 1)
	addCodeRows(exactRows, 0)
	ranked := make([]rankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ranked = append(ranked, candidate)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].tier != ranked[j].tier {
			return ranked[i].tier < ranked[j].tier
		}
		if ranked[i].row.Score != ranked[j].row.Score {
			return ranked[i].row.Score > ranked[j].row.Score
		}
		return ranked[i].row.ChunkID < ranked[j].row.ChunkID
	})
	if params.TopK >= 0 && len(ranked) > params.TopK {
		ranked = ranked[:params.TopK]
	}
	results := make([]*types.IndexWithScore, len(ranked))
	for i := range ranked {
		results[i] = fromDBVectorEmbeddingWithScore(&ranked[i].row, types.MatchTypeKeywords)
		results[i].KeywordTier = ranked[i].tier
		results[i].HasKeywordTier = true
	}
	return keywordRetrieveResult(results), nil
}

func (g *pgRepository) retrieveSourceCodeCandidates(ctx context.Context, filters []clause.Expression, column string, values interface{}, query string, topK int) ([]pgVectorWithScore, error) {
	if column != "full_identifiers" && column != "normalized_terms" {
		return nil, errors.New("invalid source code search field")
	}
	operator := "&&"
	if column == "normalized_terms" {
		operator = "@>"
	}
	var rows []pgVectorWithScore
	matched := g.db.WithContext(ctx).Table("embeddings").
		Select("paradedb.score(embeddings.id) AS score, embeddings.id, embeddings.content, embeddings.source_id, embeddings.source_type, embeddings.chunk_id, embeddings.knowledge_id, embeddings.knowledge_base_id, embeddings.tag_id").
		Joins("JOIN source_chunk_search_terms ON source_chunk_search_terms.chunk_id=embeddings.chunk_id").
		Clauses(filters...).
		Where("source_chunk_search_terms."+column+" "+operator+" ?", values).
		Where("embeddings.content ||| ?", query).
		Order("score DESC, embeddings.chunk_id ASC").Limit(topK).Find(&rows)
	if matched.Error != nil {
		return nil, matched.Error
	}
	remaining := topK - len(rows)
	if remaining <= 0 {
		return rows, nil
	}
	var nonBM25 []pgVectorWithScore
	fallback := g.db.WithContext(ctx).Table("embeddings").
		Select("embeddings.id, embeddings.content, embeddings.source_id, embeddings.source_type, embeddings.chunk_id, embeddings.knowledge_id, embeddings.knowledge_base_id, embeddings.tag_id").
		Joins("JOIN source_chunk_search_terms ON source_chunk_search_terms.chunk_id=embeddings.chunk_id").
		Clauses(filters...).
		Where("source_chunk_search_terms."+column+" "+operator+" ?", values).
		Where("NOT (embeddings.content ||| ?)", query).
		Order("embeddings.chunk_id ASC").Limit(remaining).Find(&nonBM25)
	if fallback.Error != nil {
		return nil, fallback.Error
	}
	return append(rows, nonBM25...), nil
}

func keywordRetrieveResult(results []*types.IndexWithScore) []*types.RetrieveResult {
	return []*types.RetrieveResult{
		{
			Results:             results,
			RetrieverEngineType: types.PostgresRetrieverEngineType,
			RetrieverType:       types.KeywordsRetrieverType,
			Error:               nil,
		},
	}
}

// VectorRetrieve performs vector similarity search using pgvector
// Optimized to use HNSW index efficiently and avoid recalculating vector distance
func (g *pgRepository) VectorRetrieve(ctx context.Context,
	params types.RetrieveParams,
) ([]*types.RetrieveResult, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	logger.GetLogger(ctx).Infof("[Postgres] Vector retrieval: dim=%d, topK=%d, threshold=%.4f",
		len(params.Embedding), params.TopK, params.Threshold)

	dimension := len(params.Embedding)
	queryVector := pgvector.NewHalfVector(params.Embedding)

	// Build WHERE conditions for filtering
	whereParts := make([]string, 0)
	if g.sourceVisibility {
		whereParts = append(whereParts, source.PublishedChunkSQL(ctx, "embeddings.chunk_id", "embeddings.knowledge_id"))
	}
	allVars := make([]interface{}, 0)
	if len(params.SourceIDs) > 0 && !g.sourceVisibility {
		return nil, errors.New("repository source scope requires the built-in source index")
	}

	// Add query vector first (used in ORDER BY for HNSW index)
	allVars = append(allVars, queryVector)

	// Dimension filter (required for HNSW index WHERE clause)
	whereParts = append(whereParts, fmt.Sprintf("dimension = $%d", len(allVars)+1))
	allVars = append(allVars, dimension)
	if len(params.SourceIDs) > 0 {
		marks := make([]string, len(params.SourceIDs))
		for i, id := range params.SourceIDs {
			allVars = append(allVars, id)
			marks[i] = fmt.Sprintf("$%d", len(allVars))
		}
		whereParts = append(whereParts, sourceRepositoryFilterSQL(strings.Join(marks, ", ")))
	}

	// KnowledgeBaseIDs and KnowledgeIDs use AND logic
	// - If only KnowledgeBaseIDs: search entire knowledge bases
	// - If only KnowledgeIDs: search specific documents
	// - If both: search specific documents within the knowledge bases (AND)
	if len(params.KnowledgeBaseIDs) > 0 {
		logger.GetLogger(ctx).Debugf(
			"[Postgres] Filtering vector search by knowledge base IDs: %v",
			params.KnowledgeBaseIDs,
		)
		placeholders := make([]string, len(params.KnowledgeBaseIDs))
		paramStart := len(allVars) + 1
		for i := range params.KnowledgeBaseIDs {
			placeholders[i] = fmt.Sprintf("$%d", paramStart+i)
			allVars = append(allVars, params.KnowledgeBaseIDs[i])
		}
		whereParts = append(whereParts, fmt.Sprintf("knowledge_base_id IN (%s)",
			strings.Join(placeholders, ", ")))
	}
	if len(params.KnowledgeIDs) > 0 {
		logger.GetLogger(ctx).Debugf(
			"[Postgres] Filtering vector search by knowledge IDs: %v",
			params.KnowledgeIDs,
		)
		placeholders := make([]string, len(params.KnowledgeIDs))
		paramStart := len(allVars) + 1
		for i := range params.KnowledgeIDs {
			placeholders[i] = fmt.Sprintf("$%d", paramStart+i)
			allVars = append(allVars, params.KnowledgeIDs[i])
		}
		whereParts = append(whereParts, fmt.Sprintf("knowledge_id IN (%s)",
			strings.Join(placeholders, ", ")))
	}
	// Filter by tag IDs if specified
	if len(params.TagIDs) > 0 {
		logger.GetLogger(ctx).Debugf(
			"[Postgres] Filtering vector search by tag IDs: %v",
			params.TagIDs,
		)
		placeholders := make([]string, len(params.TagIDs))
		paramStart := len(allVars) + 1
		for i := range params.TagIDs {
			placeholders[i] = fmt.Sprintf("$%d", paramStart+i)
			allVars = append(allVars, params.TagIDs[i])
		}
		marks := strings.Join(placeholders, ", ")
		if g.sourceVisibility {
			whereParts = append(whereParts, sourceTagFilterSQL(marks))
		} else {
			whereParts = append(whereParts, fmt.Sprintf("tag_id IN (%s)", marks))
		}
	}

	// is_enabled filter
	whereParts = append(whereParts, fmt.Sprintf("(is_enabled IS NULL OR is_enabled = $%d)", len(allVars)+1))
	allVars = append(allVars, true)

	// Build WHERE clause string
	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = "WHERE " + strings.Join(whereParts, " AND ")
	}

	// Expand TopK to get more candidates before threshold filtering.
	//
	// HNSW requires `ef_search >= LIMIT`, and a very large LIMIT (e.g. 1000)
	// forces HNSW to walk a near-exhaustive portion of the graph, often making
	// it slower than a sequential scan and pushing the planner to pick Seq Scan
	// even when an index exists. 200 is a good sweet spot: it gives enough
	// headroom for threshold/filter post-processing without ballooning ef_search.
	expandedTopK := params.TopK * 2
	if expandedTopK < 100 {
		expandedTopK = 100 // Minimum 100 candidates
	}
	if expandedTopK > 200 {
		expandedTopK = 200 // Maximum 200 candidates (keeps HNSW efficient)
	}
	if expandedTopK < params.TopK {
		expandedTopK = params.TopK // Ensure subquery limit is at least final limit
	}

	// Optimized query: Use subquery to calculate distance once.
	//
	// IMPORTANT: The HNSW index in this project is built on the EXPRESSION
	//   (embedding::halfvec(<dim>)) halfvec_cosine_ops
	// because the `embedding` column itself is `halfvec` without a fixed dimension
	// (so the table can store multiple embedding sizes such as 798 / 3584 / ...).
	//
	// pgvector requires the ORDER BY expression to match the indexed expression
	// EXACTLY, otherwise the planner falls back to a sequential scan. The
	// `embedding::halfvec(%d)` cast on both sides of `<=>` is therefore NOT
	// redundant — it is the only way to make the HNSW index get used at all.
	// See: pgvector issues #702, #835 and ParadeDB "indexing-expressions" docs.
	subqueryLimitParam := len(allVars) + 1
	thresholdParam := len(allVars) + 2
	finalLimitParam := len(allVars) + 3
	queryPrefix, candidateTable := "", "embeddings"
	if g.sourceVisibility && (len(params.SourceIDs) > 0 || source.HasPinnedSources(ctx)) {
		// HNSW's global candidate walk can exhaust its approximation budget
		// before a sparse manifest/file/tag filter finds a valid source. Rank
		// the authorized relation itself; it must precede both candidate LIMITs.
		queryPrefix = "WITH source_candidates AS MATERIALIZED (SELECT * FROM embeddings " + whereClause + ") "
		candidateTable, whereClause = "source_candidates", ""
	}

	querySQL := queryPrefix + fmt.Sprintf(`
		SELECT 
			id, content, source_id, source_type, chunk_id, knowledge_id, knowledge_base_id, tag_id,
			(1 - distance) as score
		FROM (
			SELECT 
				id, content, source_id, source_type, chunk_id, knowledge_id, knowledge_base_id, tag_id,
				embedding::halfvec(%[1]d) <=> $1::halfvec(%[1]d) as distance
			FROM %[6]s
			%[2]s
			ORDER BY embedding::halfvec(%[1]d) <=> $1::halfvec(%[1]d)
			LIMIT $%[3]d
		) AS candidates
		WHERE distance <= $%[4]d
		ORDER BY distance ASC
		LIMIT $%[5]d
	`, dimension, whereClause, subqueryLimitParam, thresholdParam, finalLimitParam, candidateTable)

	allVars = append(allVars, expandedTopK)       // LIMIT in subquery
	allVars = append(allVars, 1-params.Threshold) // Distance threshold
	allVars = append(allVars, params.TopK)        // Final LIMIT

	// HNSW's `ef_search` defaults to 40, which is much smaller than our
	// `expandedTopK` budget (up to 1000). Without raising it, HNSW would only
	// return ~40 candidates per scan and our outer threshold/filter step would
	// silently lose recall. `SET LOCAL` requires a transaction so we wrap the
	// query in one. We never write inside this transaction, so the cost is
	// negligible.
	efSearch := expandedTopK
	if efSearch < 40 {
		efSearch = 40
	}

	var embeddingDBList []pgVectorWithScore

	err := g.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("SET LOCAL hnsw.ef_search = %d", efSearch)).Error; err != nil {
			// Treat as non-fatal: pgvector should always expose this GUC, but if
			// for any reason it does not we still want the query to run (just
			// with default recall). We must rollback first because a failed
			// statement aborts the transaction in PostgreSQL.
			logger.GetLogger(ctx).Warnf("[Postgres] Failed to set hnsw.ef_search=%d: %v", efSearch, err)
			return err
		}
		// pgvector >= 0.8 supports iterative scan, which keeps pulling more
		// candidates from HNSW until the post-filter (knowledge_base_id /
		// knowledge_id / tag_id / is_enabled) yields enough rows. Without it,
		// HNSW returns at most ef_search candidates and the outer filter may
		// silently lose recall when the filter is selective.
		// Best-effort: ignore failure on older pgvector versions.
		if err := tx.Exec("SET LOCAL hnsw.iterative_scan = strict_order").Error; err != nil {
			logger.GetLogger(ctx).Debugf("[Postgres] hnsw.iterative_scan not available: %v", err)
			// abort transaction and let the fallback path below handle it.
			return err
		}
		return tx.Raw(querySQL, allVars...).Scan(&embeddingDBList).Error
	})

	// Fallback: if the transaction failed because of an unsupported GUC (e.g.
	// older pgvector that doesn't have hnsw.ef_search or hnsw.iterative_scan),
	// retry the query without the SETs so we still return results.
	if err != nil && len(embeddingDBList) == 0 &&
		(strings.Contains(err.Error(), "hnsw.ef_search") ||
			strings.Contains(err.Error(), "hnsw.iterative_scan")) {
		logger.GetLogger(ctx).Warnf("[Postgres] Retrying vector query without HNSW GUC overrides: %v", err)
		err = g.db.WithContext(ctx).Raw(querySQL, allVars...).Scan(&embeddingDBList).Error
	}

	if err == gorm.ErrRecordNotFound {
		logger.GetLogger(ctx).Warnf("[Postgres] No vector matches found that meet threshold %.4f", params.Threshold)
		return nil, nil
	}
	if err != nil {
		logger.GetLogger(ctx).Errorf("[Postgres] Vector retrieval failed: %v", err)
		return nil, err
	}

	// Apply final TopK limit (in case we got more results than needed)
	if len(embeddingDBList) > int(params.TopK) {
		embeddingDBList = embeddingDBList[:params.TopK]
	}

	logger.GetLogger(ctx).Infof("[Postgres] Vector retrieval found %d results", len(embeddingDBList))
	results := make([]*types.IndexWithScore, len(embeddingDBList))
	const maxVectorResultLog = 8
	for i := range embeddingDBList {
		results[i] = fromDBVectorEmbeddingWithScore(&embeddingDBList[i], types.MatchTypeEmbedding)
		if i < maxVectorResultLog {
			logger.GetLogger(ctx).Debugf("[Postgres] Vector search result %d: chunk_id %s, score %.4f",
				i, results[i].ChunkID, results[i].Score)
		}
	}
	if len(results) > maxVectorResultLog {
		logger.GetLogger(ctx).Debugf(
			"[Postgres] Vector search result summary: total=%d logged=%d truncated=%d",
			len(results), maxVectorResultLog, len(results)-maxVectorResultLog,
		)
	}
	return []*types.RetrieveResult{
		{
			Results:             results,
			RetrieverEngineType: types.PostgresRetrieverEngineType,
			RetrieverType:       types.VectorRetrieverType,
			Error:               nil,
		},
	}, nil
}

// CopyIndices copies index data
func (g *pgRepository) CopyIndices(ctx context.Context,
	sourceKnowledgeBaseID string,
	sourceToTargetKBIDMap map[string]string,
	sourceToTargetChunkIDMap map[string]string,
	targetKnowledgeBaseID string,
	dimension int,
	knowledgeType string,
) error {
	logger.GetLogger(ctx).Infof(
		"[Postgres] Copying indices, source knowledge base: %s, target knowledge base: %s, mapping count: %d",
		sourceKnowledgeBaseID, targetKnowledgeBaseID, len(sourceToTargetChunkIDMap),
	)

	if len(sourceToTargetChunkIDMap) == 0 {
		logger.GetLogger(ctx).Warnf("[Postgres] Mapping is empty, no need to copy")
		return nil
	}

	// Batch processing parameters
	batchSize := 500 // Number of records to process per batch
	offset := 0      // Offset for pagination
	totalCopied := 0 // Total number of copied records

	for {
		// Paginated query for source data
		var sourceVectors []*pgVector
		if err := g.db.WithContext(ctx).
			Where("knowledge_base_id = ?", sourceKnowledgeBaseID).
			Limit(batchSize).
			Offset(offset).
			Find(&sourceVectors).Error; err != nil {
			logger.GetLogger(ctx).Errorf("[Postgres] Failed to query source index data: %v", err)
			return err
		}

		// If no more data, exit the loop
		if len(sourceVectors) == 0 {
			if offset == 0 {
				logger.GetLogger(ctx).Warnf("[Postgres] No source index data found")
			}
			break
		}

		batchCount := len(sourceVectors)
		logger.GetLogger(ctx).Infof(
			"[Postgres] Found %d source index data, batch start position: %d",
			batchCount, offset,
		)

		// Create target vector index
		targetVectors := make([]*pgVector, 0, batchCount)
		for _, sourceVector := range sourceVectors {
			// Get the mapped target chunk ID
			targetChunkID, ok := sourceToTargetChunkIDMap[sourceVector.ChunkID]
			if !ok {
				logger.GetLogger(ctx).Warnf(
					"[Postgres] Source chunk %s not found in target chunk mapping, skipping",
					sourceVector.ChunkID,
				)
				continue
			}

			// Get the mapped target knowledge ID
			targetKnowledgeID, ok := sourceToTargetKBIDMap[sourceVector.KnowledgeID]
			if !ok {
				logger.GetLogger(ctx).Warnf(
					"[Postgres] Source knowledge %s not found in target knowledge mapping, skipping",
					sourceVector.KnowledgeID,
				)
				continue
			}

			// Handle SourceID transformation for generated questions
			// Generated questions have SourceID format: {chunkID}-{questionID}
			// Regular chunks have SourceID == ChunkID
			var targetSourceID string
			if sourceVector.SourceID == sourceVector.ChunkID {
				// Regular chunk, use targetChunkID as SourceID
				targetSourceID = targetChunkID
			} else if strings.HasPrefix(sourceVector.SourceID, sourceVector.ChunkID+"-") {
				// This is a generated question, preserve the questionID part
				questionID := strings.TrimPrefix(sourceVector.SourceID, sourceVector.ChunkID+"-")
				targetSourceID = fmt.Sprintf("%s-%s", targetChunkID, questionID)
			} else {
				// For other complex scenarios, generate new unique SourceID
				targetSourceID = uuid.New().String()
			}

			// Create new vector index, copy the content and vector of the source index
			targetVector := &pgVector{
				Content:         sourceVector.Content,
				SourceID:        targetSourceID, // Handle SourceID transformation properly
				SourceType:      sourceVector.SourceType,
				ChunkID:         targetChunkID,         // Update to target chunk ID
				KnowledgeID:     targetKnowledgeID,     // Update to target knowledge ID
				KnowledgeBaseID: targetKnowledgeBaseID, // Update to target knowledge base ID
				Dimension:       sourceVector.Dimension,
				Embedding:       sourceVector.Embedding, // Copy the vector embedding directly, avoid recalculation
			}

			targetVectors = append(targetVectors, targetVector)
		}

		// Batch insert target vector index
		if len(targetVectors) > 0 {
			if err := g.db.WithContext(ctx).
				Clauses(clause.OnConflict{DoNothing: true}).Create(targetVectors).Error; err != nil {
				logger.GetLogger(ctx).Errorf("[Postgres] Failed to batch create target index: %v", err)
				return err
			}

			totalCopied += len(targetVectors)
			logger.GetLogger(ctx).Infof(
				"[Postgres] Successfully copied batch data, batch size: %d, total copied: %d",
				len(targetVectors),
				totalCopied,
			)
		}

		// Move to the next batch
		offset += batchCount

		// If the number of returned records is less than the requested size, it means the last page has been reached
		if batchCount < batchSize {
			break
		}
	}

	logger.GetLogger(ctx).Infof("[Postgres] Index copying completed, total copied: %d", totalCopied)
	return nil
}

// BatchUpdateChunkEnabledStatus updates the enabled status of chunks in batch
func (g *pgRepository) BatchUpdateChunkEnabledStatus(ctx context.Context, chunkStatusMap map[string]bool) error {
	if len(chunkStatusMap) == 0 {
		logger.GetLogger(ctx).Warnf("[Postgres] Chunk status map is empty, skipping update")
		return nil
	}

	logger.GetLogger(ctx).Infof("[Postgres] Batch updating chunk enabled status, count: %d", len(chunkStatusMap))

	// Group chunks by enabled status for batch updates
	enabledChunkIDs := make([]string, 0)
	disabledChunkIDs := make([]string, 0)

	for chunkID, enabled := range chunkStatusMap {
		if enabled {
			enabledChunkIDs = append(enabledChunkIDs, chunkID)
		} else {
			disabledChunkIDs = append(disabledChunkIDs, chunkID)
		}
	}

	// Batch update enabled chunks
	if len(enabledChunkIDs) > 0 {
		result := g.db.WithContext(ctx).Model(&pgVector{}).
			Where("chunk_id IN ?", enabledChunkIDs).
			Update("is_enabled", true)
		if result.Error != nil {
			logger.GetLogger(ctx).Errorf("[Postgres] Failed to update enabled chunks: %v", result.Error)
			return result.Error
		}
		logger.GetLogger(ctx).
			Infof("[Postgres] Updated %d chunks to enabled, rows affected: %d", len(enabledChunkIDs), result.RowsAffected)
	}

	// Batch update disabled chunks
	if len(disabledChunkIDs) > 0 {
		result := g.db.WithContext(ctx).Model(&pgVector{}).
			Where("chunk_id IN ?", disabledChunkIDs).
			Update("is_enabled", false)
		if result.Error != nil {
			logger.GetLogger(ctx).Errorf("[Postgres] Failed to update disabled chunks: %v", result.Error)
			return result.Error
		}
		logger.GetLogger(ctx).
			Infof("[Postgres] Updated %d chunks to disabled, rows affected: %d", len(disabledChunkIDs), result.RowsAffected)
	}

	logger.GetLogger(ctx).Infof("[Postgres] Successfully batch updated chunk enabled status")
	return nil
}

// BatchUpdateChunkTagID updates the tag ID of chunks in batch
func (g *pgRepository) BatchUpdateChunkTagID(ctx context.Context, chunkTagMap map[string]string) error {
	if len(chunkTagMap) == 0 {
		logger.GetLogger(ctx).Warnf("[Postgres] Chunk tag map is empty, skipping update")
		return nil
	}

	logger.GetLogger(ctx).Infof("[Postgres] Batch updating chunk tag ID, count: %d", len(chunkTagMap))

	// Group chunks by tag ID for batch updates
	tagGroups := make(map[string][]string)
	for chunkID, tagID := range chunkTagMap {
		tagGroups[tagID] = append(tagGroups[tagID], chunkID)
	}

	// Batch update chunks for each tag ID
	for tagID, chunkIDs := range tagGroups {
		result := g.db.WithContext(ctx).Model(&pgVector{}).
			Where("chunk_id IN ?", chunkIDs).
			Update("tag_id", tagID)
		if result.Error != nil {
			logger.GetLogger(ctx).Errorf("[Postgres] Failed to update chunks with tag_id %s: %v", tagID, result.Error)
			return result.Error
		}
		logger.GetLogger(ctx).
			Infof("[Postgres] Updated %d chunks to tag_id=%s, rows affected: %d", len(chunkIDs), tagID, result.RowsAffected)
	}

	logger.GetLogger(ctx).Infof("[Postgres] Successfully batch updated chunk tag ID")
	return nil
}
