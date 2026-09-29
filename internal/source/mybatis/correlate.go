package mybatis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Correlate joins only files supplied for one fixed snapshot. It never treats
// a same-named mapper from another path as a match, and ambiguous/missing
// targets are represented as uncertain relations instead of guessed edges.
func Correlate(input CorrelationInput) ([]Relation, []string) {
	warnings := []string{}
	namespaceDocs := map[string][]XMLDocument{}
	for _, doc := range input.XML {
		namespaceDocs[doc.Namespace] = append(namespaceDocs[doc.Namespace], doc)
		if doc.Namespace == "" {
			warnings = append(warnings, fmt.Sprintf("%s has no mapper namespace", doc.Path))
		}
	}
	for namespace, docs := range namespaceDocs {
		if namespace != "" && len(docs) > 1 {
			warnings = append(warnings, fmt.Sprintf("duplicate mapper namespace %q (%d documents)", namespace, len(docs)))
		}
	}
	relations := []Relation{}
	for _, java := range input.Java {
		docs := namespaceDocs[java.QualifiedName]
		for _, method := range java.Methods {
			if len(docs) != 1 {
				relations = append(relations, uncertainRelation(input, RelationMapperStatement, java.Path, method.Range, java.QualifiedName+"#"+method.Name, "", typesRangeZero(), "", "mapper namespace is missing or ambiguous"))
				continue
			}
			doc := docs[0]
			matches := []Statement{}
			for _, statement := range doc.Statements {
				if statement.ID == method.Name {
					matches = append(matches, statement)
				}
			}
			if len(matches) != 1 {
				reason := "mapper statement target is missing"
				if len(matches) > 1 {
					reason = "mapper statement target is duplicated"
				}
				relations = append(relations, uncertainRelation(input, RelationMapperStatement, java.Path, method.Range, java.QualifiedName+"#"+method.Name, doc.Path, doc.Range, method.Name, reason))
				continue
			}
			statement := matches[0]
			relations = append(relations, Relation{Kind: RelationMapperStatement, FromPath: java.Path, FromRange: method.Range, FromKey: java.QualifiedName + "#" + method.Name, ToPath: doc.Path, ToRange: statement.Range, ToKey: doc.Namespace + "." + statement.ID, Determinacy: "certain", Quality: statement.Quality, Context: statement.Context, SnapshotID: input.SnapshotID, CommitSHA: input.CommitSHA})
			for _, ref := range statement.ResultMapRefs {
				matches := findResultMaps(doc, ref)
				if len(matches) == 1 {
					relations = append(relations, Relation{Kind: RelationResultMap, FromPath: doc.Path, FromRange: statement.Range, FromKey: statement.ID, ToPath: doc.Path, ToRange: matches[0].Range, ToKey: ref, Determinacy: "certain", Quality: "structural", SnapshotID: input.SnapshotID, CommitSHA: input.CommitSHA})
				} else {
					relations = append(relations, uncertainRelation(input, RelationResultMap, doc.Path, statement.Range, statement.ID, doc.Path, doc.Range, ref, "resultMap target is missing or duplicated"))
				}
			}
			for _, ref := range statement.IncludeRefs {
				matches := findIncludes(doc, ref)
				if len(matches) == 1 {
					relations = append(relations, Relation{Kind: RelationInclude, FromPath: doc.Path, FromRange: statement.Range, FromKey: statement.ID, ToPath: doc.Path, ToRange: matches[0].Range, ToKey: ref, Determinacy: "certain", Quality: "structural", SnapshotID: input.SnapshotID, CommitSHA: input.CommitSHA})
				} else {
					relations = append(relations, uncertainRelation(input, RelationInclude, doc.Path, statement.Range, statement.ID, doc.Path, doc.Range, ref, "sql include target is missing or duplicated"))
				}
			}
			for _, table := range statement.Tables {
				determinacy, quality := "certain", "structural"
				if !table.Certain {
					determinacy, quality = "uncertain", "dynamic SQL has branch-dependent table access"
				}
				relations = append(relations, Relation{Kind: RelationTableAccess, FromPath: doc.Path, FromRange: table.Range, FromKey: statement.ID, ToKey: table.Name, Determinacy: determinacy, Quality: quality, SnapshotID: input.SnapshotID, CommitSHA: input.CommitSHA})
			}
		}
	}
	sort.SliceStable(relations, func(i, j int) bool { return relationSortKey(relations[i]) < relationSortKey(relations[j]) })
	sort.Strings(warnings)
	return relations, warnings
}

func findResultMaps(doc XMLDocument, id string) []ResultMap {
	result := []ResultMap{}
	for _, item := range doc.ResultMaps {
		if item.ID == id {
			result = append(result, item)
		}
	}
	return result
}
func findIncludes(doc XMLDocument, id string) []Include {
	result := []Include{}
	for _, item := range doc.Includes {
		if item.ID == id {
			result = append(result, item)
		}
	}
	return result
}
func typesRangeZero() (zero types.SourceRange) { return zero }

func uncertainRelation(input CorrelationInput, kind, fromPath string, fromRange types.SourceRange, fromKey, toPath string, toRange types.SourceRange, toKey, reason string) Relation {
	return Relation{Kind: kind, FromPath: fromPath, FromRange: fromRange, FromKey: fromKey, ToPath: toPath, ToRange: toRange, ToKey: toKey, Determinacy: "uncertain", Quality: "uncertain", Context: []types.SourceContext{{Text: reason, Range: toRange}}, SnapshotID: input.SnapshotID, CommitSHA: input.CommitSHA}
}

func relationSortKey(relation Relation) string {
	return strings.Join([]string{relation.Kind, relation.FromPath, fmt.Sprint(relation.FromRange.StartByte), relation.ToPath, relation.ToKey}, "|")
}
