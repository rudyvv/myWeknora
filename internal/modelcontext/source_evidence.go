package modelcontext

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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
	attrs := fmt.Sprintf(` source_id="%s" snapshot_id="%s" file_version_id="%s" project_id="%s" commit_sha="%s" path="%s" start_line="%d" end_line="%d" source_url="%s"`,
		escapeAttr(evidence.DataSourceID), escapeAttr(evidence.SnapshotID), escapeAttr(evidence.FileVersionID),
		escapeAttr(evidence.ProjectID), escapeAttr(evidence.CommitSHA), escapeAttr(evidence.Path),
		evidence.Range.StartLine, evidence.Range.EndLine, escapeAttr(evidence.GitLabURL))
	quality := boundedSourceQuality(evidence.Quality)
	regionQuality := ""
	if evidence.Region != nil {
		regionQuality = boundedSourceQuality(evidence.Region.Quality)
	}
	if quality == "" {
		quality = regionQuality
	}
	if quality != "" {
		attrs += fmt.Sprintf(` quality="%s"`, escapeAttr(quality))
	}
	if evidence.Region != nil {
		if kind := boundedSourceValue(evidence.Region.Kind, 32); kind != "" {
			attrs += fmt.Sprintf(` region_kind="%s"`, escapeAttr(kind))
		}
		if language := boundedSourceValue(evidence.Region.Language, 64); language != "" {
			attrs += fmt.Sprintf(` region_language="%s"`, escapeAttr(language))
		}
		if status := boundedExternalStatus(evidence.Region.ExternalStatus); status != "" {
			attrs += fmt.Sprintf(` external_status="%s"`, status)
		}
		if regionQuality != "" && regionQuality != quality {
			attrs += fmt.Sprintf(` region_quality="%s"`, escapeAttr(regionQuality))
		}
	}
	if symbols := boundedSourceSymbols(evidence.Symbols); symbols != "" {
		attrs += fmt.Sprintf(` symbols="%s"`, escapeAttr(symbols))
	}
	if diagnostics := boundedSourceDiagnostics(evidence.Diagnostics); diagnostics != "" {
		attrs += fmt.Sprintf(` sfc_diagnostics="%s"`, escapeAttr(diagnostics))
	}
	return attrs
}

func boundedExternalStatus(value string) string {
	if len(value) > 16 {
		return ""
	}
	switch value {
	case "unchecked", "rejected":
		return value
	default:
		return ""
	}
}

func boundedSourceDiagnostics(diagnostics []types.SourceDiagnostic) string {
	values := make([]string, 0, min(len(diagnostics), 4))
	for index, diagnostic := range diagnostics {
		if len(values) == 4 || index == 16 {
			break
		}
		if diagnostic.Range.StartLine < 1 || diagnostic.Range.EndLine < diagnostic.Range.StartLine {
			continue
		}
		var explanation string
		switch diagnostic.Code {
		case "vue_sfc_parse_warning":
			explanation = "Vue SFC block has a descriptor warning; original bytes are retained."
		case "vue_sfc_duplicate_block":
			explanation = "Vue SFC has a duplicate top-level block; original bytes are retained."
		default:
			continue
		}
		values = append(values, fmt.Sprintf("%s@L%d-L%d: %s", diagnostic.Code,
			diagnostic.Range.StartLine, diagnostic.Range.EndLine, explanation))
	}
	return boundedSourceValue(strings.Join(values, "; "), 512)
}

const (
	modelSourceSymbolCountMax    = 8
	modelSourceSymbolRunesMax    = 80
	modelSourceSymbolsMax        = 512
	modelSourceSymbolScanMax     = 32
	modelSourceValueScanMaxRunes = 512
)

func boundedSourceQuality(value string) string {
	switch value {
	case "structural", "syntax_error", "degraded", "unknown_preprocess", "text_fallback":
		return value
	default:
		return ""
	}
}

func boundedSourceValue(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	var b strings.Builder
	space, written, scanned := false, 0, 0
	for _, r := range value {
		scanned++
		if scanned > modelSourceValueScanMaxRunes {
			break
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if b.Len() > 0 {
				space = true
			}
			continue
		}
		if space {
			if written+1 >= maxRunes {
				break
			}
			b.WriteByte(' ')
			written++
			space = false
		}
		if written >= maxRunes {
			break
		}
		b.WriteRune(r)
		written++
	}
	return b.String()
}

func boundedSourceSymbols(symbols []string) string {
	bounded := make([]string, 0, min(len(symbols), modelSourceSymbolCountMax))
	remaining := modelSourceSymbolsMax
	for index, symbol := range symbols {
		if len(bounded) == modelSourceSymbolCountMax || remaining <= 0 || index == modelSourceSymbolScanMax {
			break
		}
		value := boundedSourceValue(symbol, modelSourceSymbolRunesMax)
		if value == "" {
			continue
		}
		if len(bounded) > 0 {
			remaining--
		}
		if runes := []rune(value); len(runes) > remaining {
			value = string(runes[:remaining])
		}
		bounded = append(bounded, value)
		remaining -= utf8.RuneCountInString(value)
	}
	return strings.Join(bounded, " ")
}
