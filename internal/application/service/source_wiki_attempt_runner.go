package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

var (
	errSourceWikiAttemptCallContext  = errors.New("provider request exceeds the fixed model context window")
	errSourceWikiProviderCallFailed  = errors.New("source Wiki provider call failed")
	errSourceWikiAttemptModelChanged = errors.New("source Wiki attempt model settings changed")
)

const sourceWikiProviderMaxAttempts = 3

type sourceWikiChatModel interface {
	Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error)
}

// sourceWikiAttemptCallRunner owns the one allowed model-dispatch path for an
// attempt: context preflight, durable reservation, lease renewal, dispatch,
// and usage settlement. Source generation/QA stages provide their own messages
// but cannot bypass the persisted ledger.
type sourceWikiAttemptCallRunner struct {
	ledger                   *repository.SourceWikiAttemptLedger
	lease                    types.SourceWikiAttemptLease
	model                    sourceWikiChatModel
	modelID                  string
	modelSettingsFingerprint string
	leaseFor                 time.Duration
	now                      func() time.Time
}

func (r *sourceWikiAttemptCallRunner) Call(ctx context.Context, phase string, messages []chat.Message) (*types.ChatResponse, error) {
	if r == nil || r.ledger == nil || r.model == nil || r.lease.AttemptID == "" || r.lease.MaxCompletionTokens <= 0 || r.leaseFor <= 0 || phase == "" {
		return nil, fmt.Errorf("invalid source Wiki attempt call configuration")
	}
	if r.modelID == "" || r.modelID != r.lease.ModelID || r.modelSettingsFingerprint == "" || r.modelSettingsFingerprint != r.lease.ModelSettingsFingerprint {
		return nil, errSourceWikiAttemptModelChanged
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("encode source Wiki provider request: %w", err)
	}
	reservedTokens := len(encoded) + r.lease.MaxCompletionTokens
	if r.lease.ModelContextWindow > 0 && reservedTokens > r.lease.ModelContextWindow {
		return nil, errSourceWikiAttemptCallContext
	}
	now := r.clock()
	reservation, err := r.ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{
		Lease: r.lease, Phase: phase, ReservedTokens: reservedTokens, Now: now, LeaseFor: r.leaseFor,
	})
	if err != nil {
		return nil, err
	}

	response, callErr, heartbeatErr := r.dispatchWithHeartbeat(ctx, messages)
	if heartbeatErr != nil {
		// The reservation stays charged. A newer epoch owns recovery and the
		// old worker must not persist any late provider result.
		return nil, heartbeatErr
	}
	actualTokens := responseUsage(response)
	outcome := "succeeded"
	if callErr != nil {
		outcome = "provider_error"
	} else if response == nil {
		outcome = "unknown"
		callErr = errSourceWikiProviderCallFailed
	}
	if settleErr := r.ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{
		Lease: r.lease, ReservationID: reservation.ID, Outcome: outcome,
		ActualTokens: actualTokens, Now: r.clock(),
	}); settleErr != nil {
		return nil, settleErr
	}
	if callErr != nil {
		return nil, errSourceWikiProviderCallFailed
	}
	return response, nil
}

func (r *sourceWikiAttemptCallRunner) CallWithRetries(ctx context.Context, phase string, messages []chat.Message, maxAttempts int) (*types.ChatResponse, error) {
	if maxAttempts < 1 {
		return nil, fmt.Errorf("provider retry limit must allow at least one dispatch")
	}
	if maxAttempts > sourceWikiProviderMaxAttempts {
		maxAttempts = sourceWikiProviderMaxAttempts
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		response, err := r.Call(ctx, phase, messages)
		if err == nil {
			return response, nil
		}
		if !errors.Is(err, errSourceWikiProviderCallFailed) {
			return nil, err
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (r *sourceWikiAttemptCallRunner) dispatchWithHeartbeat(ctx context.Context, messages []chat.Message) (*types.ChatResponse, error, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interval := r.leaseFor / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	heartbeatFailure := make(chan error, 1)
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-callCtx.Done():
				return
			case <-ticker.C:
				if err := r.ledger.Renew(ctx, r.lease, r.clock(), r.leaseFor); err != nil {
					select {
					case heartbeatFailure <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	response, callErr := r.model.Chat(callCtx, messages, &chat.ChatOptions{Temperature: 0, MaxTokens: r.lease.MaxCompletionTokens})
	close(stopHeartbeat)
	cancel()
	<-heartbeatDone
	select {
	case heartbeatErr := <-heartbeatFailure:
		return nil, nil, heartbeatErr
	default:
	}
	return response, callErr, nil
}

func (r *sourceWikiAttemptCallRunner) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func responseUsage(response *types.ChatResponse) *int {
	if response == nil || response.Usage.TotalTokens <= 0 {
		return nil
	}
	usage := response.Usage.TotalTokens
	return &usage
}
