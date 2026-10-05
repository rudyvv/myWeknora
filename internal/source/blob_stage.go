package source

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// BlobStage keeps selected original bytes off the Go heap between fetch and
// parse. Its private generated directory is removed on Close.
type BlobStage struct {
	mu     sync.Mutex
	root   string
	limit  int64
	used   int64
	closed bool
}

func NewBlobStage(limit int64) (*BlobStage, error) {
	if limit <= 0 {
		return nil, ErrResourceLimitExceeded
	}
	root, err := os.MkdirTemp("", "weknora-source-blobs-")
	if err != nil {
		return nil, fmt.Errorf("source staging storage unavailable")
	}
	if err := os.Chmod(root, 0700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("source staging storage unavailable")
	}
	return &BlobStage{root: root, limit: limit}, nil
}

func (s *BlobStage) Put(key string, content []byte) error {
	if s == nil {
		return fmt.Errorf("source staging is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("source staging is closed")
	}
	if int64(len(content)) > s.limit-s.used {
		return ErrResourceLimitExceeded
	}
	digest := sha256.Sum256([]byte(key))
	path := filepath.Join(s.root, hex.EncodeToString(digest[:]))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("source staging write failed")
	}
	written, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil || written != len(content) || closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("source staging write failed")
	}
	s.used += int64(len(content))
	return nil
}

func (s *BlobStage) Read(key string, maxBytes int64) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("source staging is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || maxBytes < 0 {
		return nil, fmt.Errorf("source staging is unavailable")
	}
	digest := sha256.Sum256([]byte(key))
	file, err := os.Open(filepath.Join(s.root, hex.EncodeToString(digest[:])))
	if err != nil {
		return nil, fmt.Errorf("source staged blob is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("source staged blob exceeds its file budget")
	}
	return data, nil
}

func (s *BlobStage) UsedBytes() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

func (s *BlobStage) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := os.RemoveAll(s.root); err != nil {
		return fmt.Errorf("source staging cleanup failed")
	}
	return nil
}
