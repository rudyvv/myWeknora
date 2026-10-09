package source

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tiktoken-go/tokenizer"
)

// This opt-in check runs the production Go token-budget adapter against the
// locked HTTP worker. It makes no repository, embedding or database requests.
func TestSemanticParserWithRealTokenBudget(t *testing.T) {
	endpoint := os.Getenv("WEKNORA_SOURCE_PARSER_TEST_URL")
	if endpoint == "" {
		t.Skip("set WEKNORA_SOURCE_PARSER_TEST_URL to the locked parser worker")
	}
	fields := strings.Repeat("value: 1,\n", 400)
	statements := strings.Repeat("    value += 1\n", 300)
	cases := map[string]string{
		"src/store.js":   "let store = new Vuex.Store({state: {\n" + fields + "}});\nexport default store;\n",
		"src/config.ts":  "export const config = {\n" + fields + "};\n",
		"src/view.tsx":   "export const View = () => <section>\n" + strings.Repeat("<p>value</p>\n", 300) + "</section>;\n",
		"src/Main.java":  "class Main { int calculate(int value) {\n" + strings.Repeat("value += 1;\n", 300) + "return value;\n}}\n",
		"src/main.py":    "@trace\nasync def calculate(value):\n" + statements + "    return value\n",
		"src/view.vue":   "<template><p>value</p></template>\n<script>let store = new Vuex.Store({state: {\n" + fields + "}});</script>\n",
		"src/Mapper.xml": "<mapper namespace=\"demo.Mapper\">\n<select id=\"first\">SELECT 1</select>\n<select id=\"second\">SELECT 2</select>\n</mapper>\n",
		"config/.env":    strings.Repeat("NAME=value\n", 200),
	}
	fragments := map[string]bool{"let": true, "const": true, "export": true, "store =": true, "new Vuex.Store": true, "(": true, ")": true, "{": true, "}": true, ";": true}
	for path, content := range cases {
		t.Run(path, func(t *testing.T) {
			profile := IndexProfile{Tokenizer: tokenizer.Cl100kBase, MaxTokens: 240}
			parsed, err := ParseFileWithProfile(context.Background(), endpoint, path, []byte(content), profile)
			require.NoError(t, err)
			var rebuilt strings.Builder
			for _, chunk := range parsed.Chunks {
				rebuilt.WriteString(chunk.Content)
				trimmed := strings.TrimSpace(chunk.Content)
				require.NotEmpty(t, trimmed)
				require.False(t, fragments[trimmed], "isolated expression fragment")
				count, err := profile.CountTokens(SourceIndexText(path, chunk))
				require.NoError(t, err)
				require.LessOrEqual(t, count, profile.MaxTokens)
			}
			require.Equal(t, content, rebuilt.String())
		})
	}
}
