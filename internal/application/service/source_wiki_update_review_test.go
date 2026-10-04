package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestSourceWikiNextImpactIndexBuildsRouteDependenciesOnce(t *testing.T) {
	snapshot := types.SourceWikiImpactSnapshot{
		SourceID: "source-a", SnapshotID: "snapshot-next",
		Members: []types.SourceWikiImpactMember{
			{SourceFileID: "file-a", Status: "parsed", Facts: []types.ParsedSourceFact{{Kind: "api_request", HTTPMethod: "get", RoutePath: "/orders"}}},
			{SourceFileID: "file-b", Status: "parsed", Generated: true, Facts: []types.ParsedSourceFact{{Kind: "api_request", HTTPMethod: "GET", RoutePath: "/orders"}}},
			{SourceFileID: "file-c", Status: "excluded", Facts: []types.ParsedSourceFact{{Kind: "api_request", HTTPMethod: "GET", RoutePath: "/orders"}}},
		},
	}
	budget := &sourceWikiUpdateInventoryBudget{}
	index, err := buildSourceWikiNextImpactIndex(snapshot, budget)
	if err != nil {
		t.Fatal(err)
	}
	got, valid, err := sourceWikiImpactDependenciesForNext(types.SourceWikiTopic{
		SourceID: "source-a", SnapshotID: "snapshot-next", TopicKey: "flow/GET /orders", Kind: "flow",
	}, sourceWikiUpdateTopic{}, index, budget)
	if err != nil || !valid {
		t.Fatalf("dependencies valid=%v err=%v", valid, err)
	}
	if !reflect.DeepEqual(got, []string{"file-a"}) {
		t.Fatalf("dependencies = %v, want [file-a]", got)
	}
}

func TestSourceWikiUpdateInventoryBudgetRejectsWorkAndReferences(t *testing.T) {
	for name, budget := range map[string]*sourceWikiUpdateInventoryBudget{
		"work":       {work: sourceWikiUpdateInventoryMaxWork},
		"references": {refs: sourceWikiUpdateInventoryMaxRefs},
	} {
		t.Run(name, func(t *testing.T) {
			work, refs := 1, 0
			if name == "references" {
				work, refs = 0, 1
			}
			if err := budget.consume(work, refs); !errors.Is(err, errSourceWikiUpdateInventoryBudgetExceeded) {
				t.Fatalf("consume error = %v, want bounded-work error", err)
			}
		})
	}
}

func TestSourceWikiBodyCompositionRequiresExactAttributedParts(t *testing.T) {
	parts := []string{"first attributed body", "second attributed body"}
	if !sourceWikiBodyCompositionMatches("second attributed body\nfirst attributed body", parts) {
		t.Fatal("exact independent bodies in either order should be attributable")
	}
	if sourceWikiBodyCompositionMatches("first attributed body\nmanual note\nsecond attributed body", parts) {
		t.Fatal("manual text must make the body unattributable")
	}
	if sourceWikiBodyCompositionMatches("first attributed body\nsecond attributed body\n", parts) {
		t.Fatal("extra trailing text must make the body unattributable")
	}
}

func TestSourceWikiImpactLoadFallbackClassifiesOnlyDeterministicProofErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "proof incomplete", err: &repository.SourceWikiImpactLoadError{Failure: repository.SourceWikiImpactLoadFailureProofIncomplete, ReasonCode: repository.SourceWikiImpactReasonManifestIncomplete}, want: "impact_load_proof_incomplete_snapshot_manifest_incomplete"},
		{name: "invalid snapshot", err: &repository.SourceWikiImpactLoadError{Failure: repository.SourceWikiImpactLoadFailureInvalid, ReasonCode: repository.SourceWikiImpactReasonParsedFactsInvalid}, want: "impact_load_snapshot_invalid_parsed_facts_invalid"},
		{name: "budget exceeded", err: &repository.SourceWikiImpactLoadError{Failure: repository.SourceWikiImpactLoadFailureBudgetExceeded, ReasonCode: repository.SourceWikiImpactReasonFactCountExceeded}, want: "impact_load_budget_exceeded_fact_count_exceeded"},
		{name: "operational", err: errors.New("connection reset")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := sourceWikiImpactLoadFallbackReason(test.err)
			if test.want == "" {
				if ok {
					t.Fatalf("operational error classified as durable fallback %q", got)
				}
				return
			}
			if !ok || got != test.want {
				t.Fatalf("fallback = %q, %v; want %q", got, ok, test.want)
			}
		})
	}
}
