// Package source reads repository objects without checking out or executing
// target code. It does not create knowledge, indexes, or Wiki pages.
package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// PreviewGit inventories a commit using an isolated, temporary bare object
// database. A loopback transport bridge keeps Git HTTP inside the application's
// existing redirect and dial-time SSRF policy; credentials never enter command
// arguments, Git configuration, environment variables, or subprocess output.
func PreviewGit(ctx context.Context, repository *types.SourceRepository, rules *datasource.SourceSettings) ([]types.SourcePreviewFile, error) {
	return ReadGit(ctx, repository, rules, nil)
}

// ReadGit visits verified, selected text blobs while the isolated object
// database is alive, and returns the complete inventory. The caller decides
// whether the entire inventory can be committed; callbacks never imply publish.
func ReadGit(ctx context.Context, repository *types.SourceRepository, rules *datasource.SourceSettings, consume func(types.SourcePreviewFile, []byte) error) ([]types.SourcePreviewFile, error) {
	return ReadGitWithPolicy(ctx, repository, rules, DefaultResourcePolicy(), consume)
}

// ReadGitWithPolicy applies finite whole-run selection and transfer budgets
// before reading any selected blobs. Manifest failures never return a partial
// inventory that could be mistaken for a complete snapshot.
func ReadGitWithPolicy(ctx context.Context, repository *types.SourceRepository, rules *datasource.SourceSettings, policy ResourcePolicy, consume func(types.SourcePreviewFile, []byte) error) ([]types.SourcePreviewFile, error) {
	files, _, err := ReadGitWithPolicyMetrics(ctx, repository, rules, policy, consume)
	return files, err
}

type GitReadMetrics struct {
	TransferBytes         int64
	ObjectStageBytes      int64
	SelectedBytes         int64
	TransferBytesMeasured bool
	ObjectStageMeasured   bool
	SelectedBytesMeasured bool
	CleanupResidueCount   int64
}

func ReadGitWithPolicyMetrics(ctx context.Context, repository *types.SourceRepository, rules *datasource.SourceSettings, policy ResourcePolicy, consume func(types.SourcePreviewFile, []byte) error) ([]types.SourcePreviewFile, GitReadMetrics, error) {
	metrics := GitReadMetrics{}
	files, err := readGitWithPolicy(ctx, repository, rules, policy, consume, &metrics)
	return files, metrics, err
}

func readGitWithPolicy(ctx context.Context, repository *types.SourceRepository, rules *datasource.SourceSettings, policy ResourcePolicy, consume func(types.SourcePreviewFile, []byte) error, metrics *GitReadMetrics) ([]types.SourcePreviewFile, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := CheckTemporaryStorageCapacity(policy); err != nil {
		return nil, err
	}
	if repository == nil || rules == nil || len(rules.Projects) == 0 {
		return nil, fmt.Errorf("invalid source repository settings")
	}
	rules = policy.ApplyFileLimit(rules)
	ctx, cancel := context.WithTimeout(ctx, policy.RunTimeout)
	defer cancel()
	root, err := os.MkdirTemp("", "weknora-source-preview-")
	if err != nil {
		return nil, fmt.Errorf("source preview storage unavailable")
	}
	var transferCounter *atomic.Int64
	defer func() {
		if metrics != nil {
			if transferCounter != nil {
				metrics.TransferBytes = transferCounter.Load()
				metrics.TransferBytesMeasured = true
			}
			if size, sizeErr := directorySize(root); sizeErr == nil {
				metrics.ObjectStageBytes = size
				metrics.ObjectStageMeasured = true
			}
		}
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil && metrics != nil {
			metrics.CleanupResidueCount++
		}
	}() // root is the absolute directory returned by MkdirTemp.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("Git is not installed on the server")
	}
	command := func(args ...string) *exec.Cmd {
		base := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "http.proxy=", "-c", "protocol.allow=never", "-c", "protocol.http.allow=always", "-c", "submodule.recurse=false"}
		cmd := exec.CommandContext(ctx, gitPath, append(base, args...)...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "TEMP=" + root, "TMP=" + root, "HOME=" + root,
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_COUNT=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_LFS_SKIP_SMUDGE=1"}
		return cmd
	}
	if err := command("init", "--bare", "--template=").Run(); err != nil {
		return nil, fmt.Errorf("unable to initialize source preview object database")
	}
	remote, closeBridge, transferred, transferExceeded, err := bridgeGit(ctx, repository, policy.GitTransferBytes)
	if err != nil {
		return nil, err
	}
	transferCounter = transferred
	defer closeBridge()
	fetch := command("fetch", "--no-tags", "--no-write-fetch-head", "--depth=1", remote, repository.CommitSHA)
	if err := runGitWithObjectStageLimit(ctx, fetch, root, policy.GitObjectStageBytes, policy); err != nil {
		if transferExceeded.Load() {
			return nil, ErrResourceLimitExceeded
		}
		if errors.Is(err, ErrStorageBudgetExceeded) || errors.Is(err, ErrResourceLimitExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("unable to fetch the fixed GitLab commit; check read_repository permission and branch availability")
	}
	var inventory bytes.Buffer
	cmd := command("ls-tree", "-r", "-l", "-z", "--full-tree", repository.CommitSHA)
	cmd.Stdout = &limitedWriter{writer: &inventory, remaining: 32 << 20}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("unable to read complete source manifest")
	}
	files := make([]types.SourcePreviewFile, 0)
	selectedFiles := 0
	var selectedBytes int64
	for _, entry := range bytes.Split(inventory.Bytes(), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		header, filePath, ok := bytes.Cut(entry, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 4 || !utf8.Valid(filePath) {
			return nil, fmt.Errorf("source manifest contains an unreadable path")
		}
		file := types.SourcePreviewFile{Path: string(filePath), BlobSHA: fields[2], Status: "included", Reason: "selected by source rules"}
		file.Size, _ = strconv.ParseInt(fields[3], 10, 64)
		switch {
		case !matches(file.Path, rules.Projects[0].Paths, true):
			file.Status, file.Reason = "excluded", "outside included paths"
		case matches(file.Path, rules.ExcludePaths, false):
			file.Status, file.Reason = "excluded", "matched excluded path"
		case fields[1] == "commit":
			file.Status, file.Reason = "submodule_unavailable", "submodule content is not fetched; separate repository authorization is required"
		case fields[0] == "120000":
			file.Status, file.Reason = "symlink_unavailable", "symbolic links are not followed"
		case fields[1] != "blob" || file.Size < 0:
			file.Status, file.Reason = "unreadable", "unsupported Git object"
		case file.Size > rules.MaxFileBytes:
			file.Status, file.Reason = "oversize", "exceeds configured max_file_bytes"
		}
		files = append(files, file)
		if file.Status == "included" {
			selectedFiles++
			if file.Size > policy.MaxSelectedBytes-selectedBytes {
				return nil, ErrResourceLimitExceeded
			}
			selectedBytes += file.Size
			if selectedFiles > policy.MaxSelectedFiles {
				return nil, ErrResourceLimitExceeded
			}
		}
		if len(files) > 100000 {
			return nil, fmt.Errorf("source manifest exceeds the preview file limit")
		}
	}
	// One cat-file process reads all selected blobs; no per-file process spawn.
	batch := command("cat-file", "--batch")
	input, err := batch.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("source object reader unavailable")
	}
	output, err := batch.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("source object reader unavailable")
	}
	if err := batch.Start(); err != nil {
		return nil, fmt.Errorf("source object reader unavailable")
	}
	defer func() { _ = input.Close(); _ = batch.Process.Kill(); _ = batch.Wait() }()
	reader := bufio.NewReader(output)
	var consumedBytes int64
	for i := range files {
		file := &files[i]
		if file.Status != "included" {
			continue
		}
		if _, err := fmt.Fprintln(input, file.BlobSHA); err != nil {
			return nil, fmt.Errorf("source object read failed")
		}
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[0] != file.BlobSHA || fields[1] != "blob" || fields[2] != strconv.FormatInt(file.Size, 10) {
			return nil, fmt.Errorf("source object manifest mismatch")
		}
		content := make([]byte, file.Size)
		if _, err := io.ReadFull(reader, content); err != nil {
			return nil, fmt.Errorf("source object read failed")
		}
		if separator, err := reader.ReadByte(); err != nil || separator != '\n' {
			return nil, fmt.Errorf("source object framing failed")
		}
		classify(file, content)
		if file.Status == "included" {
			consumedBytes += file.Size
		}
		if consume != nil && file.Status == "included" {
			if err := consume(*file, content); err != nil {
				return nil, err
			}
		}
	}
	if metrics != nil {
		metrics.SelectedBytes = consumedBytes
		metrics.SelectedBytesMeasured = true
	}
	return files, nil
}

func matches(file string, paths []string, emptyDefault bool) bool {
	if len(paths) == 0 {
		return emptyDefault
	}
	for _, prefix := range paths {
		if file == prefix || strings.HasPrefix(file, prefix+"/") {
			return true
		}
	}
	return false
}

func classify(file *types.SourcePreviewFile, content []byte) {
	if bytes.HasPrefix(content, []byte("version https://git-lfs.github.com/spec/v1\n")) || bytes.HasPrefix(content, []byte("version https://git-lfs.github.com/spec/v1\r\n")) {
		file.Status, file.Reason = "lfs_unavailable", "Git LFS pointer only; LFS objects are not fetched"
		return
	}
	if bytes.HasPrefix(content, []byte{0xff, 0xfe}) || bytes.HasPrefix(content, []byte{0xfe, 0xff}) {
		file.Encoding = "utf-16le"
		if content[0] == 0xfe {
			file.Encoding = "utf-16be"
		}
		file.Status, file.Reason = "unsupported_encoding", "UTF-16 source bytes are available but require parser encoding support"
		return
	}
	if bytes.IndexByte(content, 0) >= 0 {
		file.Status, file.Reason = "binary", "binary or unsupported text encoding"
		return
	}
	if !utf8.Valid(content) {
		file.Status, file.Reason = "unknown_encoding", "text is not valid UTF-8; no encoding is guessed"
		return
	}
	file.Encoding = "utf-8"
	if bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		file.Encoding = "utf-8-bom"
	}
	header := content
	if len(header) > 8192 {
		header = header[:8192]
	}
	lower := bytes.ToLower(header)
	file.Generated = bytes.Contains(lower, []byte("@generated")) || bytes.Contains(lower, []byte("automatically generated")) || bytes.Contains(lower, []byte("do not edit"))
	if file.Generated {
		file.Reason = "included; generated-code marker detected"
	}
	if strings.HasSuffix(strings.ToLower(file.Path), ".min.js") || strings.HasSuffix(strings.ToLower(file.Path), ".min.css") {
		file.Status, file.Reason = "excluded", "minified asset"
	}
}

func bridgeGit(ctx context.Context, repository *types.SourceRepository, transferLimit int64) (string, func(), *atomic.Int64, *atomic.Bool, error) {
	upstream, err := url.Parse(repository.CloneURL)
	if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" {
		return "", nil, nil, nil, fmt.Errorf("invalid GitLab clone URL")
	}
	if err := datasource.ValidateConnectorBaseURL(upstream.String()); err != nil {
		return "", nil, nil, nil, fmt.Errorf("GitLab repository URL is blocked by SSRF policy")
	}
	client := datasource.NewConnectorHTTPClient(90 * time.Second)
	var transferred atomic.Int64
	var transferExceeded atomic.Bool
	// Redirects cannot extend the authorized repository or leak its credentials.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("source Git transport unavailable")
	}
	prefix := "/" + uuid.NewString() + "/repo.git"
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suffix := strings.TrimPrefix(r.URL.Path, prefix)
		if (r.URL.Path != prefix+"/info/refs" || r.Method != http.MethodGet || r.URL.RawQuery != "service=git-upload-pack") &&
			(r.URL.Path != prefix+"/git-upload-pack" || r.Method != http.MethodPost || r.URL.RawQuery != "") {
			http.NotFound(w, r)
			return
		}
		target := *upstream
		target.Path = path.Join(upstream.Path, suffix)
		target.RawPath, target.RawQuery = "", r.URL.RawQuery
		req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			http.Error(w, "source Git request failed", http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		req.SetBasicAuth("oauth2", repository.Token)
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "source Git request failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "GitLab repository is unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		if _, err := io.Copy(&gitTransferWriter{writer: w, transferred: &transferred, exceeded: &transferExceeded, limit: transferLimit}, resp.Body); err != nil {
			return
		}
	})}
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + prefix, func() { _ = server.Close(); client.CloseIdleConnections() }, &transferred, &transferExceeded, nil
}

type gitTransferWriter struct {
	writer      io.Writer
	transferred *atomic.Int64
	exceeded    *atomic.Bool
	limit       int64
}

func (w *gitTransferWriter) Write(data []byte) (int, error) {
	for {
		current := w.transferred.Load()
		if int64(len(data)) > w.limit-current {
			if w.exceeded != nil {
				w.exceeded.Store(true)
			}
			return 0, ErrResourceLimitExceeded
		}
		if w.transferred.CompareAndSwap(current, current+int64(len(data))) {
			n, err := w.writer.Write(data)
			if n < 0 || n > len(data) {
				w.transferred.Add(-int64(len(data)))
				return 0, fmt.Errorf("Git transfer writer returned an invalid byte count")
			}
			if n < len(data) {
				w.transferred.Add(int64(n) - int64(len(data)))
			}
			return n, err
		}
	}
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func runGitWithObjectStageLimit(ctx context.Context, command *exec.Cmd, root string, limit int64, policy ResourcePolicy) error {
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				return err
			}
			used, err := directorySize(root)
			if err != nil {
				return ErrStorageBudgetExceeded
			}
			if used > limit {
				return ErrStorageBudgetExceeded
			}
			if CheckFreeSpaceReserve(policy) != nil {
				return ErrStorageBudgetExceeded
			}
			return nil
		case <-ticker.C:
			used, err := directorySize(root)
			if err != nil || used > limit || CheckFreeSpaceReserve(policy) != nil {
				_ = command.Process.Kill()
				<-done
				return ErrStorageBudgetExceeded
			}
		case <-ctx.Done():
			_ = command.Process.Kill()
			<-done
			return ctx.Err()
		}
	}
}

func directorySize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() < 0 || info.Size() > int64(^uint64(0)>>1)-size {
			return ErrStorageBudgetExceeded
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("source preview output limit exceeded")
	}
	w.remaining -= int64(len(data))
	return w.writer.Write(data)
}
