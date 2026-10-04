package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestSourceWikiRemovalEvidenceUsesOneCompleteSnapshotIndex(t *testing.T) {
	snapshot := types.SourceWikiImpactSnapshot{
		TenantID: 1, KnowledgeBaseID: "kb", SourceID: "source", SnapshotID: "next",
		Stage: types.SourceWikiImpactPublishedComplete, ManifestComplete: true, ExpectedMemberCount: 1,
		RelationsComplete: true, ExpectedRelationCount: 0,
		Members: []types.SourceWikiImpactMember{{
			Path: "src/handler.java", SourceFileID: "file", FileVersionID: "version", Status: "parsed",
			Quality: "structural", FactsComplete: true, ExpectedFactCount: 1,
			Facts: []types.ParsedSourceFact{{Kind: "api_request", HTTPMethod: "GET", RoutePath: "/orders", Quality: "structural"}},
		}},
	}
	loaded := &repository.SourceWikiSkeletonSnapshot{
		TenantID: 1, DataSourceID: "source", SnapshotID: "next", Complete: true,
		Members: []repository.SourceWikiSkeletonFactMember{{FileID: "file", VersionID: "version", Path: "src/handler.java"}},
	}
	evidence := sourceWikiBuildRemovalEvidence(snapshot, loaded)
	if !sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{TopicKey: "module/old", Kind: "module", ModulePath: "old", SourceOwned: true}, evidence) {
		t.Fatal("complete manifest should prove the absent module")
	}
	if sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{TopicKey: "module/src", Kind: "module", ModulePath: "src", SourceOwned: true}, evidence) {
		t.Fatal("module with a manifest member must not be removed")
	}
	if !sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{TopicKey: "flow/GET /missing", Kind: "flow", SourceOwned: true}, evidence) {
		t.Fatal("complete structural route evidence should prove the absent flow")
	}
	if sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{TopicKey: "flow/GET /orders", Kind: "flow", SourceOwned: true}, evidence) {
		t.Fatal("a route present in parser facts must not be removed")
	}
}

func TestSourceWikiRemovalEvidenceFailsClosedOnUncertainFlowEvidence(t *testing.T) {
	snapshot := types.SourceWikiImpactSnapshot{
		TenantID: 1, KnowledgeBaseID: "kb", SourceID: "source", SnapshotID: "next",
		ManifestComplete: true, ExpectedMemberCount: 1, RelationsComplete: true,
		Members: []types.SourceWikiImpactMember{{
			Path: "src/handler.java", SourceFileID: "file", FileVersionID: "version", Status: "parsed",
			Quality: "structural", FactsComplete: true,
			Facts: []types.ParsedSourceFact{{Kind: "api_prefix", Quality: "semantic", Dynamic: true}},
		}},
	}
	loaded := &repository.SourceWikiSkeletonSnapshot{Complete: true}
	evidence := sourceWikiBuildRemovalEvidence(snapshot, loaded)
	if sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{TopicKey: "flow/GET /orders", Kind: "flow", SourceOwned: true}, evidence) {
		t.Fatal("uncertain route evidence must block a flow-removal proof")
	}
	if sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{TopicKey: "flow/GET /orders", Kind: "flow", SourceOwned: false}, sourceWikiRemovalEvidence{flowComplete: true}) {
		t.Fatal("manual topics must never be removed by source absence")
	}
}
