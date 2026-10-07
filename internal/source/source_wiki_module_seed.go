package source

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// SourceWikiModuleFactPriority classifies parser-authored facts for module
// seed selection. Repository projections and Wiki skeleton planning share this
// pure selector so SQL parity can be verified against one classifier.
func SourceWikiModuleFactPriority(fact types.ParsedSourceFact) int {
	if fact.Quality != "" && fact.Quality != "structural" {
		return 0
	}
	switch fact.Kind {
	case "spring_mapping":
		return 110
	case "mybatis_mapper", "mybatis_statement", "mybatis_result_map":
		return 75
	case "java_type":
		name := strings.ToLower(fact.Name)
		for _, role := range []string{"controller", "service", "serviceimpl", "mapper", "repository", "configuration", "config", "application"} {
			if strings.HasSuffix(name, role) {
				return 90
			}
		}
	}
	return 0
}
