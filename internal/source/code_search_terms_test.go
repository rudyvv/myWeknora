package source

import (
	"testing"
)

func TestParseSourceCodeQueryRecognizesIdentifiersAndPathsButNotChineseBusinessText(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		enabled bool
		exact   string
		terms   []string
	}{
		{name: "pure Chinese", query: "如何查询用户预约记录", enabled: false},
		{name: "plain English", query: "how to update the reservation record", enabled: false},
		{name: "camel and qualified identifier", query: "请定位 `demo.PushScheduleMapper#selectById`", enabled: true, exact: "demo.PushScheduleMapper#selectById", terms: []string{"push", "schedule", "mapper", "select", "by", "id"}},
		{name: "snake identifier", query: "find_by_reservation_id", enabled: true, exact: "find_by_reservation_id", terms: []string{"find", "by", "reservation", "id"}},
		{name: "path", query: "src/main/java/demo/UserMapper.xml", enabled: true, exact: "src/main/java/demo/UserMapper.xml", terms: []string{"src", "main", "java", "demo", "user", "mapper", "xml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSourceCodeQuery(tt.query)
			if got.Enabled != tt.enabled {
				t.Fatalf("Enabled = %v, want %v (query %q)", got.Enabled, tt.enabled, tt.query)
			}
			if !tt.enabled {
				return
			}
			if !containsString(got.ExactIdentifiers, tt.exact) {
				t.Errorf("exact identifiers %q do not contain %q", got.ExactIdentifiers, tt.exact)
			}
			for _, term := range tt.terms {
				if !containsString(got.NormalizedTerms, term) {
					t.Errorf("normalized terms %q do not contain %q", got.NormalizedTerms, term)
				}
			}
		})
	}
}

func TestParseSourceCodeQueryIsBounded(t *testing.T) {
	query := ParseSourceCodeQuery("UserMapper.findById " + repeated("VeryLongIdentifier_", 80))
	if len(query.ExactIdentifiers) > MaxSourceCodeQueryIdentifiers {
		t.Fatalf("exact identifiers exceeded bound: %d", len(query.ExactIdentifiers))
	}
	if len(query.NormalizedTerms) > MaxSourceCodeQueryTerms {
		t.Fatalf("normalized terms exceeded bound: %d", len(query.NormalizedTerms))
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func repeated(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}
