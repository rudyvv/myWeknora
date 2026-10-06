package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

const sourceDispatchMaxConcurrency = 4

var sourceVolcengineEmbeddingSlots = semaphore.NewWeighted(sourceDispatchMaxConcurrency)

type sourceBatchEmbedder interface {
	BatchEmbed(context.Context, []string) ([][]float32, error)
}

// sourceBatchEmbed keeps native provider batching except for Volcengine, whose
// source-indexing path benefits from bounded single-text requests.
func sourceBatchEmbed(ctx context.Context, provider string, model sourceBatchEmbedder, texts []string) ([][]float32, error) {
	if provider != string(types.ModelSourceVolcengine) {
		return model.BatchEmbed(ctx, texts)
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	group, workCtx := errgroup.WithContext(ctx)
	results := make([][]float32, len(texts))
	jobs := make(chan int)
	workers := min(sourceDispatchMaxConcurrency, len(texts))
	for range workers {
		group.Go(func() error {
			for {
				select {
				case <-workCtx.Done():
					return nil
				case index, ok := <-jobs:
					if !ok {
						return nil
					}
					if workCtx.Err() != nil {
						return nil
					}
					vector, err := sourceVolcengineEmbedOne(workCtx, model, texts[index])
					if err != nil {
						return err
					}
					results[index] = vector
				}
			}
		})
	}

schedule:
	for index := range texts {
		if workCtx.Err() != nil {
			break
		}
		select {
		case <-workCtx.Done():
			break schedule
		case jobs <- index:
		}
	}
	close(jobs)
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func sourceVolcengineEmbedOne(ctx context.Context, model sourceBatchEmbedder, text string) ([]float32, error) {
	if err := sourceVolcengineEmbeddingSlots.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	defer sourceVolcengineEmbeddingSlots.Release(1)

	vectors, err := model.BatchEmbed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("Volcengine single-text embedding returned %d vectors", len(vectors))
	}
	return vectors[0], nil
}
