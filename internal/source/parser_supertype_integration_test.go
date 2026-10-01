//go:build integration

package source

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestUnresolvedJavaSupertypeHTTPToGoConsumptionStaysNonNavigable(t *testing.T) {
	python := os.Getenv("SOURCE_TEST_PYTHON")
	cache := os.Getenv("SOURCE_PARSER_CACHE")
	if python == "" || cache == "" {
		t.Skip("SOURCE_TEST_PYTHON and SOURCE_PARSER_CACHE are required for the real parser HTTP integration")
	}
	endpoint := startSupertypeParserHTTP(t, python, cache)

	parse := func(path, content string) SourceRelationMember {
		t.Helper()
		parsed, err := ParseFile(context.Background(), endpoint, path, []byte(content))
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", path, err)
		}
		return SourceRelationMember{Path: path, FileID: strings.ReplaceAll(path, "/", "-"),
			VersionID: strings.ReplaceAll(path, "/", "-") + "-v1", Facts: parsed.Facts}
	}

	workerSource := "package app;\nimport left.*;\nimport right.*;\n" +
		"public class Worker implements Contract { public String fetch() { return \"\"; } }\n"
	worker := parse("src/app/Worker.java", workerSource)
	var reference *types.ParsedSourceFact
	for i := range worker.Facts {
		if worker.Facts[i].Kind == "java_supertype_reference" {
			reference = &worker.Facts[i]
			break
		}
	}
	if reference == nil {
		t.Fatal("Java parser HTTP response lost the unresolved implements type name")
	}
	if reference.Name != "Contract" || reference.TargetName != "Contract" || reference.Namespace != "app.Worker" ||
		reference.ReferenceKind != "implements" || reference.Certainty != "uncertain" ||
		reference.Reason != "Java supertype identity is unresolved across wildcard imports" {
		t.Fatalf("unexpected unresolved supertype fact: %#v", *reference)
	}
	if reference.Range.StartByte < 0 || reference.Range.EndByte > len(workerSource) ||
		workerSource[reference.Range.StartByte:reference.Range.EndByte] != "Contract" || reference.Text != "Contract" {
		t.Fatalf("supertype fact does not point to its exact original declaration: %#v", *reference)
	}

	left := parse("src/left/Contract.java", "package left; public interface Contract { String fetch(); }")
	right := parse("src/right/Contract.java", "package right; public interface Contract { String fetch(); }")
	for _, test := range []struct {
		name    string
		members []SourceRelationMember
	}{
		{name: "single snapshot candidate", members: []SourceRelationMember{worker, left}},
		{name: "ambiguous same-name interfaces", members: []SourceRelationMember{worker, left, right}},
	} {
		t.Run(test.name, func(t *testing.T) {
			relations := CorrelateSourceFacts(1, "source", "snapshot", test.members)
			var typeCandidate, methodCandidate bool
			for _, relation := range relations {
				if relation.Kind == "type_supertype" && relation.FromPath == worker.Path {
					typeCandidate = true
					if relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ToVersionID != "" || relation.ToPath != "" || relation.ResolutionReason == "" {
						t.Errorf("unresolved supertype became navigable or lost its reason: %#v", relation)
					}
				}
				if relation.Kind == "implements_method" && relation.FromPath == left.Path {
					methodCandidate = true
					if relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ToVersionID != "" || relation.ToPath != "" || relation.ResolutionReason == "" {
						t.Errorf("unresolved implementation candidate became navigable or lost its reason: %#v", relation)
					}
				}
			}
			if !typeCandidate {
				t.Error("Go correlation did not preserve an uncertain type-supertype candidate")
			}
			if !methodCandidate {
				t.Error("Go correlation did not preserve an uncertain interface-method candidate")
			}
		})
	}
}

func startSupertypeParserHTTP(t *testing.T, python, cache string) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate parser server relative to integration test")
	}
	serverPath, err := filepath.Abs(filepath.Join(filepath.Dir(testFile), "..", "..", "sourceparser", "server.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, serverPath, "--host", "127.0.0.1", "--port", "0")
	command.Env = append(os.Environ(), "SOURCE_PARSER_CACHE="+cache)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start parser HTTP service: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read parser HTTP startup response: %v (%s)", err, stderr.String())
	}
	var ready struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal([]byte(line), &ready); err != nil || ready.Port < 1 {
		t.Fatalf("invalid parser HTTP startup response %q: %v", strings.TrimSpace(line), err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", ready.Port)
}
