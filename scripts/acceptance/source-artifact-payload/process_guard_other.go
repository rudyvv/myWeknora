//go:build !windows

package main

import (
	"context"
	"errors"
)

func enumeratePinnedBackendImages(context.Context, string) ([]string, error) {
	return nil, errors.New("pinned backend process enumeration requires Windows")
}
