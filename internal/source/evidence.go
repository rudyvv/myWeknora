package source

import (
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/types"
)

// Evidence reads immutable code provenance from a source chunk. Editable
// document metadata is never used to construct a code citation.
func Evidence(metadata types.JSON) *types.SourceEvidence {
	var value struct {
		Source *types.SourceEvidence `json:"source"`
	}
	if json.Unmarshal(metadata, &value) != nil || value.Source == nil ||
		value.Source.SnapshotID == "" || value.Source.FileVersionID == "" || value.Source.CommitSHA == "" {
		return nil
	}
	return value.Source
}

// ContentIdentity keeps identical code in different repository files distinct
// when existing retrieval pipelines remove duplicate passages.
func ContentIdentity(metadata types.JSON) string {
	if evidence := Evidence(metadata); evidence != nil {
		return evidence.SnapshotID + ":" + evidence.FileVersionID + ":"
	}
	return ""
}
