package source

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type sourceWikiContributionIdentity struct {
	sourceID string
	kind     string
	key      string
}

// ReplaceSourceWikiContribution applies one explicit outcome to one source/topic
// contribution. It does not render WikiPage content, infer deletion, or mutate
// its input. Contributions are returned in stable source/kind/key order.
func ReplaceSourceWikiContribution(
	existing types.SourceWikiContributionSet,
	target types.SourceWikiContributionTarget,
	outcome types.SourceWikiContributionOutcome,
) (types.SourceWikiContributionSet, error) {
	if existing.TenantID == 0 || strings.TrimSpace(existing.KnowledgeBaseID) == "" {
		return existing, fmt.Errorf("source Wiki contribution set lacks tenant or knowledge-base identity")
	}
	if target.TenantID != existing.TenantID || target.KnowledgeBaseID != existing.KnowledgeBaseID {
		return existing, fmt.Errorf("source Wiki contribution target crosses tenant or knowledge-base boundaries")
	}
	if strings.TrimSpace(target.SourceID) == "" || strings.TrimSpace(target.TopicKind) == "" ||
		strings.TrimSpace(target.TopicKey) == "" || strings.TrimSpace(target.SnapshotID) == "" {
		return existing, fmt.Errorf("source Wiki contribution target has incomplete identity")
	}
	if existing.HasUnattributedBody {
		return existing, fmt.Errorf("source Wiki page has unattributed mixed body and cannot be safely replaced")
	}
	if err := validateSourceWikiContributionSet(existing); err != nil {
		return existing, err
	}
	if err := validateSourceWikiContributionOutcome(outcome, target); err != nil {
		return existing, err
	}

	result := cloneSourceWikiContributionSet(existing)
	identity := sourceWikiContributionIdentity{sourceID: target.SourceID, kind: target.TopicKind, key: target.TopicKey}
	index := -1
	for i, contribution := range result.Contributions {
		if sourceWikiContributionIdentityOf(contribution) == identity {
			index = i
			break
		}
	}

	switch outcome.Kind {
	case types.SourceWikiContributionOutcomeReplace:
		replacement := cloneSourceWikiContribution(*outcome.Replacement)
		if index == -1 {
			result.Contributions = append(result.Contributions, replacement)
		} else {
			result.Contributions[index] = replacement
		}
	case types.SourceWikiContributionOutcomeUnchanged:
		if index == -1 {
			return existing, fmt.Errorf("source Wiki contribution does not exist for unchanged outcome")
		}
		result.Contributions[index].ApplicableSnapshotID = target.SnapshotID
		result.Contributions[index].State = types.SourceWikiContributionCurrent
		result.Contributions[index].FailureReason = ""
	case types.SourceWikiContributionOutcomeGenerationFailed:
		if index == -1 {
			return existing, fmt.Errorf("source Wiki contribution does not exist for failed generation outcome")
		}
		result.Contributions[index].ApplicableSnapshotID = target.SnapshotID
		result.Contributions[index].State = types.SourceWikiContributionStale
		result.Contributions[index].FailureReason = outcome.FailureReason
	case types.SourceWikiContributionOutcomeConfirmedDeleted:
		if index != -1 {
			result.Contributions = append(result.Contributions[:index], result.Contributions[index+1:]...)
		}
	default:
		return existing, fmt.Errorf("source Wiki contribution outcome is unsupported")
	}
	if err := validateSourceWikiContributionSet(result); err != nil {
		return existing, err
	}

	sort.Slice(result.Contributions, func(i, j int) bool {
		a, b := result.Contributions[i], result.Contributions[j]
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		if a.TopicKind != b.TopicKind {
			return a.TopicKind < b.TopicKind
		}
		return a.TopicKey < b.TopicKey
	})
	return result, nil
}

func validateSourceWikiContributionSet(set types.SourceWikiContributionSet) error {
	if len(set.Contributions) > types.SourceWikiContributionMaxCount {
		return fmt.Errorf("source Wiki contribution set exceeds its count limit")
	}
	seen := make(map[sourceWikiContributionIdentity]struct{}, len(set.Contributions))
	totalBodyBytes := 0
	for _, contribution := range set.Contributions {
		if err := validateSourceWikiContribution(contribution); err != nil {
			return err
		}
		identity := sourceWikiContributionIdentityOf(contribution)
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("source Wiki contribution set has duplicate source/topic identity")
		}
		seen[identity] = struct{}{}
		totalBodyBytes += len(contribution.Body)
		if totalBodyBytes > types.SourceWikiContributionMaxTotalBodyBytes {
			return fmt.Errorf("source Wiki contribution set exceeds its total body limit")
		}
	}
	return nil
}

func validateSourceWikiContribution(contribution types.SourceWikiContribution) error {
	if strings.TrimSpace(contribution.SourceID) == "" || strings.TrimSpace(contribution.TopicKind) == "" ||
		strings.TrimSpace(contribution.TopicKey) == "" || strings.TrimSpace(contribution.OriginSnapshotID) == "" ||
		strings.TrimSpace(contribution.ApplicableSnapshotID) == "" {
		return fmt.Errorf("source Wiki contribution has incomplete source/topic/snapshot identity")
	}
	if !utf8.ValidString(contribution.Body) || strings.TrimSpace(contribution.Body) == "" || len(contribution.Body) > types.SourceWikiContributionMaxBodyBytes {
		return fmt.Errorf("source Wiki contribution has invalid or oversized independent body")
	}
	if len(contribution.Evidence) == 0 || len(contribution.Evidence) > types.SourceWikiContributionMaxEvidencePerItem {
		return fmt.Errorf("source Wiki contribution has no evidence or exceeds its evidence limit")
	}
	if contribution.State != types.SourceWikiContributionCurrent && contribution.State != types.SourceWikiContributionStale {
		return fmt.Errorf("source Wiki contribution has an unsupported state")
	}
	if len(contribution.FailureReason) > types.SourceWikiContributionMaxFailureReasonBytes || !utf8.ValidString(contribution.FailureReason) {
		return fmt.Errorf("source Wiki contribution has an invalid failure reason")
	}
	if contribution.State == types.SourceWikiContributionCurrent && contribution.FailureReason != "" {
		return fmt.Errorf("current source Wiki contribution cannot retain a failure reason")
	}
	seenEvidence := make(map[string]struct{}, len(contribution.Evidence))
	for _, evidence := range contribution.Evidence {
		if err := validateSourceWikiContributionEvidence(evidence, contribution); err != nil {
			return err
		}
		if _, exists := seenEvidence[evidence.ID]; exists {
			return fmt.Errorf("source Wiki contribution has duplicate evidence IDs")
		}
		seenEvidence[evidence.ID] = struct{}{}
	}
	return nil
}

func validateSourceWikiContributionEvidence(evidence types.SourceWikiEvidence, contribution types.SourceWikiContribution) error {
	if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.KnowledgeID) == "" ||
		evidence.DataSourceID != contribution.SourceID || evidence.SnapshotID != contribution.OriginSnapshotID ||
		strings.TrimSpace(evidence.FileVersionID) == "" || strings.TrimSpace(evidence.Path) == "" ||
		!validSourceWikiContributionCommit(evidence.CommitSHA) || !validSourceWikiContributionSHA256(evidence.SHA256) ||
		!validSourceWikiContributionSHA256(evidence.TextSHA256) || evidence.Range.StartByte < 0 ||
		evidence.Range.EndByte <= evidence.Range.StartByte || evidence.Range.StartLine < 1 || evidence.Range.EndLine < evidence.Range.StartLine {
		return fmt.Errorf("source Wiki contribution evidence has incomplete or mismatched identity")
	}
	return nil
}

func validSourceWikiContributionSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSourceWikiContributionCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateSourceWikiContributionOutcome(outcome types.SourceWikiContributionOutcome, target types.SourceWikiContributionTarget) error {
	switch outcome.Kind {
	case types.SourceWikiContributionOutcomeReplace:
		if outcome.Replacement == nil || outcome.FailureReason != "" || outcome.CompleteSnapshotID != "" {
			return fmt.Errorf("replace outcome requires only a replacement contribution")
		}
		replacement := *outcome.Replacement
		if replacement.SourceID != target.SourceID || replacement.TopicKind != target.TopicKind || replacement.TopicKey != target.TopicKey ||
			replacement.OriginSnapshotID != target.SnapshotID || replacement.ApplicableSnapshotID != target.SnapshotID ||
			replacement.State != types.SourceWikiContributionCurrent || replacement.FailureReason != "" {
			return fmt.Errorf("replacement contribution does not match its target source/topic/snapshot")
		}
		return validateSourceWikiContribution(replacement)
	case types.SourceWikiContributionOutcomeUnchanged:
		if outcome.Replacement != nil || outcome.FailureReason != "" || outcome.CompleteSnapshotID != "" {
			return fmt.Errorf("unchanged outcome cannot include a payload")
		}
	case types.SourceWikiContributionOutcomeGenerationFailed:
		if outcome.Replacement != nil || strings.TrimSpace(outcome.FailureReason) == "" ||
			len(outcome.FailureReason) > types.SourceWikiContributionMaxFailureReasonBytes || !utf8.ValidString(outcome.FailureReason) || outcome.CompleteSnapshotID != "" {
			return fmt.Errorf("failed generation outcome requires only a bounded failure reason")
		}
	case types.SourceWikiContributionOutcomeConfirmedDeleted:
		if outcome.Replacement != nil || outcome.FailureReason != "" || outcome.CompleteSnapshotID != target.SnapshotID {
			return fmt.Errorf("confirmed deletion requires the complete target snapshot identity only")
		}
	default:
		return fmt.Errorf("source Wiki contribution outcome is unsupported")
	}
	return nil
}

func sourceWikiContributionIdentityOf(contribution types.SourceWikiContribution) sourceWikiContributionIdentity {
	return sourceWikiContributionIdentity{sourceID: contribution.SourceID, kind: contribution.TopicKind, key: contribution.TopicKey}
}

func cloneSourceWikiContributionSet(existing types.SourceWikiContributionSet) types.SourceWikiContributionSet {
	result := existing
	result.Contributions = make([]types.SourceWikiContribution, len(existing.Contributions))
	for i, contribution := range existing.Contributions {
		result.Contributions[i] = cloneSourceWikiContribution(contribution)
	}
	return result
}

func cloneSourceWikiContribution(contribution types.SourceWikiContribution) types.SourceWikiContribution {
	result := contribution
	result.Evidence = make([]types.SourceWikiEvidence, len(contribution.Evidence))
	for i, evidence := range contribution.Evidence {
		result.Evidence[i] = evidence
		result.Evidence[i].Symbols = append([]string(nil), evidence.Symbols...)
		result.Evidence[i].Context = append([]types.SourceContext(nil), evidence.Context...)
		result.Evidence[i].Diagnostics = append([]types.SourceDiagnostic(nil), evidence.Diagnostics...)
		if evidence.Region != nil {
			region := *evidence.Region
			result.Evidence[i].Region = &region
		}
	}
	return result
}
