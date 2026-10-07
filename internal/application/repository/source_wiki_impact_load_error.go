package repository

import (
	"errors"
	"fmt"
)

// SourceWikiImpactLoadFailure classifies deterministic failures to materialize
// a complete source Wiki impact proof.
type SourceWikiImpactLoadFailure string

const (
	SourceWikiImpactLoadFailureProofIncomplete SourceWikiImpactLoadFailure = "proof_incomplete"
	SourceWikiImpactLoadFailureInvalid         SourceWikiImpactLoadFailure = "invalid"
	SourceWikiImpactLoadFailureBudgetExceeded  SourceWikiImpactLoadFailure = "budget_exceeded"
)

// SourceWikiImpactLoadReasonCode is a stable, non-sensitive explanation code
// attached to a deterministic source Wiki impact load failure.
type SourceWikiImpactLoadReasonCode string

const (
	SourceWikiImpactReasonManifestIncomplete          SourceWikiImpactLoadReasonCode = "snapshot_manifest_incomplete"
	SourceWikiImpactReasonRelationsIncomplete         SourceWikiImpactLoadReasonCode = "snapshot_relations_incomplete"
	SourceWikiImpactReasonMemberCountMismatch         SourceWikiImpactLoadReasonCode = "snapshot_member_count_mismatch"
	SourceWikiImpactReasonRelationCountMismatch       SourceWikiImpactLoadReasonCode = "snapshot_relation_count_mismatch"
	SourceWikiImpactReasonMemberUnparsed              SourceWikiImpactLoadReasonCode = "snapshot_member_unparsed"
	SourceWikiImpactReasonNegativeMemberCount         SourceWikiImpactLoadReasonCode = "negative_snapshot_member_count"
	SourceWikiImpactReasonNegativeRelationCount       SourceWikiImpactLoadReasonCode = "negative_snapshot_relation_count"
	SourceWikiImpactReasonMemberCountExceeded         SourceWikiImpactLoadReasonCode = "member_count_exceeded"
	SourceWikiImpactReasonFactCountExceeded           SourceWikiImpactLoadReasonCode = "fact_count_exceeded"
	SourceWikiImpactReasonFactBytesExceeded           SourceWikiImpactLoadReasonCode = "fact_bytes_exceeded"
	SourceWikiImpactReasonMemberMetadataBytesExceeded SourceWikiImpactLoadReasonCode = "member_metadata_bytes_exceeded"
	SourceWikiImpactReasonMemberBytesMismatch         SourceWikiImpactLoadReasonCode = "member_bytes_mismatch"
	SourceWikiImpactReasonRelationCountExceeded       SourceWikiImpactLoadReasonCode = "relation_count_exceeded"
	SourceWikiImpactReasonRelationBytesExceeded       SourceWikiImpactLoadReasonCode = "relation_context_bytes_exceeded"
	SourceWikiImpactReasonRelationTotalBytesExceeded  SourceWikiImpactLoadReasonCode = "relation_total_bytes_exceeded"
	SourceWikiImpactReasonRelationInventoryChanged    SourceWikiImpactLoadReasonCode = "relation_inventory_changed"
	SourceWikiImpactReasonDuplicateMemberPath         SourceWikiImpactLoadReasonCode = "duplicate_member_path"
	SourceWikiImpactReasonParsedMemberIdentity        SourceWikiImpactLoadReasonCode = "parsed_member_identity_invalid"
	SourceWikiImpactReasonDuplicateMemberIdentity     SourceWikiImpactLoadReasonCode = "duplicate_member_identity"
	SourceWikiImpactReasonParsedFactsInvalid          SourceWikiImpactLoadReasonCode = "parsed_facts_invalid"
	SourceWikiImpactReasonExcludedMemberVersion       SourceWikiImpactLoadReasonCode = "excluded_member_version_present"
	SourceWikiImpactReasonExcludedMemberIdentity      SourceWikiImpactLoadReasonCode = "excluded_member_identity_invalid"
	SourceWikiImpactReasonUnsupportedMemberState      SourceWikiImpactLoadReasonCode = "unsupported_member_state"
)

// These sentinels let callers classify load failures with errors.Is.
var (
	ErrSourceWikiImpactProofIncomplete    = errors.New("source Wiki impact proof incomplete")
	ErrSourceWikiImpactSnapshotInvalid    = errors.New("source Wiki impact snapshot invalid")
	ErrSourceWikiImpactLoadBudgetExceeded = errors.New("source Wiki impact load budget exceeded")
)

// SourceWikiImpactLoadError identifies deterministic failures while building
// a complete impact proof. Operational database and authorization errors are
// deliberately not converted to this type.
type SourceWikiImpactLoadError struct {
	Failure    SourceWikiImpactLoadFailure
	ReasonCode SourceWikiImpactLoadReasonCode
}

func (err *SourceWikiImpactLoadError) Error() string {
	if err == nil {
		return "source Wiki impact load failed"
	}
	return fmt.Sprintf("source Wiki impact load %s: %s", err.Failure, err.ReasonCode)
}

func (err *SourceWikiImpactLoadError) Unwrap() error {
	if err == nil {
		return nil
	}
	switch err.Failure {
	case SourceWikiImpactLoadFailureProofIncomplete:
		return ErrSourceWikiImpactProofIncomplete
	case SourceWikiImpactLoadFailureInvalid:
		return ErrSourceWikiImpactSnapshotInvalid
	case SourceWikiImpactLoadFailureBudgetExceeded:
		return ErrSourceWikiImpactLoadBudgetExceeded
	default:
		return nil
	}
}

func newSourceWikiImpactLoadError(failure SourceWikiImpactLoadFailure, reason SourceWikiImpactLoadReasonCode) error {
	return &SourceWikiImpactLoadError{Failure: failure, ReasonCode: reason}
}
