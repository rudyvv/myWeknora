package source

import (
	"fmt"
	"github.com/Tencent/WeKnora/internal/types"
	"net/url"
	"strings"
)

// GitLabBlobURL anchors an escaped repository path to an immutable commit.
func GitLabBlobURL(repositoryURL, commitSHA, filePath string, coordinates types.SourceRange) string {
	parts := strings.Split(filePath, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	base := strings.TrimSuffix(strings.TrimSuffix(repositoryURL, ".git"), "/")
	return fmt.Sprintf("%s/-/blob/%s/%s#L%d-%d", base, url.PathEscape(commitSHA), strings.Join(parts, "/"), coordinates.StartLine, coordinates.EndLine)
}
