package mybatis

import "github.com/Tencent/WeKnora/internal/types"

const (
	ParserVersion = "mybatis-static-v1"

	RelationMapperStatement = "mapper_statement"
	RelationResultMap       = "result_map"
	RelationInclude         = "include"
	RelationTableAccess     = "table_access"
)

type TableAccess struct {
	Name      string
	Operation string
	Range     types.SourceRange
	Certain   bool
}

type ResultMap struct {
	ID    string
	Range types.SourceRange
}

type Include struct {
	ID    string
	Range types.SourceRange
}

type Statement struct {
	ID            string
	Kind          string
	Range         types.SourceRange
	SQLRange      types.SourceRange
	SQL           string
	Dynamic       bool
	ResultMapRefs []string
	IncludeRefs   []string
	Tables        []TableAccess
	Quality       string
	Context       []types.SourceContext
}

type XMLDocument struct {
	Path       string
	Namespace  string
	Range      types.SourceRange
	Statements []Statement
	ResultMaps []ResultMap
	Includes   []Include
	Quality    string
	Warnings   []string
}

type EmbeddedSQL struct {
	Kind     string
	SQL      string
	Range    types.SourceRange
	SQLRange types.SourceRange
	Dynamic  bool
	Certain  bool
	Tables   []TableAccess
	Quality  string
	Warnings []string
}

type MapperMethod struct {
	Name          string
	QualifiedName string
	Range         types.SourceRange
	Signature     string
	SQL           *EmbeddedSQL
}

type JavaMapper struct {
	Path          string
	QualifiedName string
	Range         types.SourceRange
	Methods       []MapperMethod
	EmbeddedSQL   []EmbeddedSQL
	Quality       string
	Warnings      []string
}

type Relation struct {
	Kind        string
	FromPath    string
	FromRange   types.SourceRange
	FromKey     string
	ToPath      string
	ToRange     types.SourceRange
	ToKey       string
	Determinacy string
	Quality     string
	Context     []types.SourceContext
	SnapshotID  string
	CommitSHA   string
}

type CorrelationInput struct {
	SnapshotID string
	CommitSHA  string
	XML        []XMLDocument
	Java       []JavaMapper
}

type Analysis struct {
	XML       XMLDocument
	Java      *JavaMapper
	Relations []Relation
	Quality   string
	Warnings  []string
}
