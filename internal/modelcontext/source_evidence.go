package modelcontext

import (
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

func sourceEvidenceValue(value interface{}) *types.SourceEvidence {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	var evidence types.SourceEvidence
	if err != nil || json.Unmarshal(data, &evidence) != nil || evidence.SnapshotID == "" || evidence.CommitSHA == "" {
		return nil
	}
	return &evidence
}

func sourceEvidenceAttrs(evidence *types.SourceEvidence) string {
	if evidence == nil {
		return ""
	}
	return fmt.Sprintf(` source_id="%s" snapshot_id="%s" file_version_id="%s" project_id="%s" commit_sha="%s" path="%s" start_line="%d" end_line="%d" source_url="%s"`,
		escapeAttr(evidence.DataSourceID), escapeAttr(evidence.SnapshotID), escapeAttr(evidence.FileVersionID),
		escapeAttr(evidence.ProjectID), escapeAttr(evidence.CommitSHA), escapeAttr(evidence.Path),
		evidence.Range.StartLine, evidence.Range.EndLine, escapeAttr(evidence.GitLabURL))
}
