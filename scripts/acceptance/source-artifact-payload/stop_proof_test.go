package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeProofRow struct {
	values []any
	err    error
}

func (row fakeProofRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return errors.New("fake row shape mismatch")
	}
	for i, value := range row.values {
		switch target := dest[i].(type) {
		case *string:
			converted, ok := value.(string)
			if !ok {
				return errors.New("fake string conversion failed")
			}
			*target = converted
		case *int64:
			converted, ok := value.(int64)
			if !ok {
				return errors.New("fake integer conversion failed")
			}
			*target = converted
		default:
			return errors.New("unsupported fake scan destination")
		}
	}
	return nil
}

type fakeProofQueryer struct {
	rows    []fakeProofRow
	queries []string
}

func (queryer *fakeProofQueryer) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	queryer.queries = append(queryer.queries, query)
	if len(queryer.rows) == 0 {
		return fakeProofRow{err: errors.New("unexpected proof query")}
	}
	row := queryer.rows[0]
	queryer.rows = queryer.rows[1:]
	return row
}

func validProofQueryer(otherSessions, prepared int64) *fakeProofQueryer {
	return &fakeProofQueryer{rows: []fakeProofRow{
		{values: []any{cloneDatabase, cloneSchema, otherSessions}},
		{values: []any{prepared}},
	}}
}

func TestWriteModesRequireExplicitStopProofBeforeAnyConnectionPath(t *testing.T) {
	for _, args := range [][]string{{"--upgrade-only"}, {"--backfill"}} {
		if _, err := parseOptions(args); err == nil {
			t.Errorf("parseOptions(%v) accepted a write mode without --stop-proof", args)
		}
	}
	for _, mode := range []string{"--upgrade-only", "--backfill"} {
		args := []string{mode, "--stop-proof=" + requiredStopProof}
		if _, err := parseOptions(args); err != nil {
			t.Errorf("parseOptions(%v) rejected the required explicit proof: %v", args, err)
		}
	}
	if _, err := parseOptions([]string{"--measure", "--stop-proof=" + requiredStopProof}); err == nil {
		t.Fatal("read-only mode accepted an unused write-proof assertion")
	}

	t.Setenv(dsnEnvName, "not-a-dsn")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--upgrade-only"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), `"arguments_rejected"`) {
		t.Fatalf("missing stop proof was not rejected at argument parsing: code=%d stderr=%q", code, stderr.String())
	}
}

func TestPinnedBackendVerifierRequiresExactPathAndRejectsHashOrEnumerationUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weknora-t22-capacity-2430a0e8.exe")
	content := []byte("pinned test executable")
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	open := func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(content)), nil }

	t.Run("exact target process exists", func(t *testing.T) {
		count, err := verifyPinnedBackendWith(context.Background(), path, sha, open,
			func(_ context.Context, base string) ([]string, error) {
				if !strings.EqualFold(base, filepath.Base(path)) {
					t.Fatalf("enumerator received base %q, want %q", base, filepath.Base(path))
				}
				return []string{path}, nil
			})
		if err != nil || count != 1 {
			t.Fatalf("exact-path process was not identified: count=%d err=%v", count, err)
		}
	})

	t.Run("same image name at another path does not count", func(t *testing.T) {
		other := filepath.Join(filepath.Dir(path), "other", filepath.Base(path))
		count, err := verifyPinnedBackendWith(context.Background(), path, sha, open,
			func(context.Context, string) ([]string, error) { return []string{other}, nil })
		if err != nil || count != 0 {
			t.Fatalf("name-only process match was accepted: count=%d err=%v", count, err)
		}
	})

	t.Run("pinned binary hash mismatch", func(t *testing.T) {
		enumerated := false
		_, err := verifyPinnedBackendWith(context.Background(), path, strings.Repeat("0", 64), open,
			func(context.Context, string) ([]string, error) { enumerated = true; return nil, nil })
		if err == nil || enumerated {
			t.Fatalf("hash mismatch did not fail before process enumeration: err=%v enumerated=%v", err, enumerated)
		}
	})

	t.Run("process enumeration unknown", func(t *testing.T) {
		_, err := verifyPinnedBackendWith(context.Background(), path, sha, open,
			func(context.Context, string) ([]string, error) { return nil, errors.New("snapshot denied") })
		if err == nil {
			t.Fatal("unknown process enumeration was treated as zero processes")
		}
	})

	t.Run("candidate image path unknown", func(t *testing.T) {
		_, err := verifyPinnedBackendWith(context.Background(), path, sha, open,
			func(context.Context, string) ([]string, error) { return []string{"relative.exe"}, nil })
		if err == nil {
			t.Fatal("unknown candidate image path was treated as no exact-path process")
		}
	})
}

func TestStopProofRejectsIdleSessionPreparedTransactionAndUnavailableStats(t *testing.T) {
	cases := []struct {
		name    string
		queryer *fakeProofQueryer
	}{
		{name: "idle extra clone session", queryer: validProofQueryer(1, 0)},
		{name: "prepared transaction", queryer: validProofQueryer(0, 1)},
		{name: "pg_stat_activity unavailable or permission denied", queryer: &fakeProofQueryer{rows: []fakeProofRow{{err: errors.New("permission denied")}}}},
		{name: "prepared transaction query unavailable", queryer: &fakeProofQueryer{rows: []fakeProofRow{{values: []any{cloneDatabase, cloneSchema, int64(0)}}, {err: errors.New("permission denied")}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifyStopProof(context.Background(), tc.queryer, func(context.Context) (int64, error) { return 0, nil })
			if err == nil {
				t.Fatal("unsafe or unobservable clone state passed stop proof")
			}
		})
	}
	queryer := validProofQueryer(0, 0)
	counts, err := verifyStopProof(context.Background(), queryer, func(context.Context) (int64, error) { return 0, nil })
	if err != nil || counts != (stopProofCounts{}) {
		t.Fatalf("complete all-zero proof rejected: counts=%+v err=%v", counts, err)
	}
	if len(queryer.queries) != 2 || strings.Contains(strings.ToLower(queryer.queries[0]), "state =") {
		t.Fatalf("session proof did not query all session states: queries=%q", queryer.queries)
	}
}

func TestOnlyCompleteStopProofInvokesWriteCallback(t *testing.T) {
	cases := []struct {
		name       string
		stopProof  string
		queryer    *fakeProofQueryer
		verify     processVerifier
		wantWrites int
	}{
		{name: "missing operator proof", stopProof: "", queryer: validProofQueryer(0, 0), verify: func(context.Context) (int64, error) { return 0, nil }},
		{name: "target process exists", stopProof: requiredStopProof, queryer: validProofQueryer(0, 0), verify: func(context.Context) (int64, error) { return 1, nil }},
		{name: "process enumeration unknown", stopProof: requiredStopProof, queryer: validProofQueryer(0, 0), verify: func(context.Context) (int64, error) { return 0, errors.New("unknown") }},
		{name: "extra idle session", stopProof: requiredStopProof, queryer: validProofQueryer(1, 0), verify: func(context.Context) (int64, error) { return 0, nil }},
		{name: "prepared transaction", stopProof: requiredStopProof, queryer: validProofQueryer(0, 1), verify: func(context.Context) (int64, error) { return 0, nil }},
		{name: "complete proof", stopProof: requiredStopProof, queryer: validProofQueryer(0, 0), verify: func(context.Context) (int64, error) { return 0, nil }, wantWrites: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			err := executeGuardedWrite(context.Background(), tc.stopProof, tc.queryer, tc.verify, nil,
				func() error { writes++; return nil })
			if tc.wantWrites == 0 && err == nil {
				t.Fatal("write callback was authorized without complete proof")
			}
			if writes != tc.wantWrites {
				t.Fatalf("write callback count=%d, want %d (err=%v)", writes, tc.wantWrites, err)
			}
		})
	}
}

func TestStopProofCountsAreReportedBeforeWriteAndReportFailureBlocksWrite(t *testing.T) {
	var report bytes.Buffer
	writes := 0
	err := executeGuardedWrite(context.Background(), requiredStopProof, validProofQueryer(0, 0),
		func(context.Context) (int64, error) { return 0, nil },
		func(counts stopProofCounts) error {
			if err := reportStopProofCounts(&report, counts); err != nil {
				return err
			}
			return nil
		},
		func() error {
			if report.Len() == 0 {
				t.Fatal("write callback ran before the safe proof counts were reported")
			}
			writes++
			return nil
		})
	if err != nil || writes != 1 {
		t.Fatalf("complete proof/report did not authorize the write: writes=%d err=%v", writes, err)
	}
	var numericCounts map[string]int64
	if err := json.Unmarshal(report.Bytes(), &numericCounts); err != nil || len(numericCounts) != 3 ||
		numericCounts["target_processes"] != 0 || numericCounts["other_clone_sessions"] != 0 || numericCounts["prepared_transactions"] != 0 {
		t.Fatalf("proof report was not exactly the safe numeric counts: %s err=%v", report.String(), err)
	}

	writes = 0
	err = executeGuardedWrite(context.Background(), requiredStopProof, validProofQueryer(0, 0),
		func(context.Context) (int64, error) { return 0, nil },
		func(counts stopProofCounts) error { return reportStopProofCounts(errorWriter{}, counts) },
		func() error { writes++; return nil })
	if err == nil || writes != 0 {
		t.Fatalf("failed proof report did not block writes: writes=%d err=%v", writes, err)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("report sink failed")
}

func TestNewProcessOrSessionBetweenBatchesStopsLaterWrites(t *testing.T) {
	tests := []struct {
		name           string
		queryer        *fakeProofQueryer
		processAppears bool
	}{
		{
			name:           "target process appears",
			queryer:        validProofQueryer(0, 0),
			processAppears: true,
		},
		{
			name: "clone session appears",
			queryer: &fakeProofQueryer{rows: []fakeProofRow{
				{values: []any{cloneDatabase, cloneSchema, int64(0)}}, {values: []any{int64(0)}},
				{values: []any{cloneDatabase, cloneSchema, int64(1)}}, {values: []any{int64(0)}},
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			processCalls := 0
			verify := processVerifier(func(context.Context) (int64, error) {
				processCalls++
				if tc.processAppears && processCalls > 1 {
					return 1, nil
				}
				return 0, nil
			})
			for batch := 0; batch < 3; batch++ {
				err := executeGuardedWrite(context.Background(), requiredStopProof, tc.queryer, verify, nil,
					func() error { writes++; return nil })
				if err != nil {
					break
				}
			}
			if writes != 1 {
				t.Fatalf("proof change did not stop later batch writes: writes=%d", writes)
			}
		})
	}
}
