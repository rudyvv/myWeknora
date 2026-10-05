package service

import (
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func ensureSourceStorage(storage map[string]types.SourceStorageMetric) map[string]types.SourceStorageMetric {
	if storage == nil {
		return make(map[string]types.SourceStorageMetric)
	}
	return storage
}

func sourceStorageMetric(usedBytes, limitBytes int64, measurement string) types.SourceStorageMetric {
	return types.SourceStorageMetric{UsedBytes: &usedBytes, LimitBytes: &limitBytes, Measurement: measurement}
}

func finishSourceTelemetryPhase(telemetry *types.SourceRunTelemetry, phase string, started *time.Time) {
	if started == nil || started.IsZero() {
		return
	}
	_ = telemetry.AddPhaseDuration(phase, time.Since(*started))
	*started = time.Time{}
}

func incrementSourceTelemetryCounter(counter **int64) {
	if counter == nil {
		return
	}
	value := int64(0)
	if *counter != nil {
		value = **counter
	}
	value++
	*counter = &value
}

type sourceEmbeddingUsage struct {
	providerCallExpected bool
	completed            bool
	attempts             atomic.Int64
	estimatedTokens      atomic.Int64
	estimateAvailable    atomic.Bool
}

func recordSourceEmbeddingUsage(telemetry *types.SourceRunTelemetry, usage *sourceEmbeddingUsage) {
	if usage == nil {
		return
	}
	calls := usage.attempts.Load()
	callsMeasured := (usage.completed && !usage.providerCallExpected) || calls > 0
	if telemetry == nil || !callsMeasured {
		return
	}
	if telemetry.ModelUsage == nil {
		telemetry.ModelUsage = &types.SourceModelUsageTelemetry{}
	}
	addSourceTelemetryCounter(&telemetry.ModelUsage.EmbeddingCalls, calls)
	if (calls == 0 && usage.completed) || (calls > 0 && usage.estimateAvailable.Load()) {
		addSourceTelemetryCounter(&telemetry.ModelUsage.EstimatedInputTokens, usage.estimatedTokens.Load())
	}
}

func addSourceTelemetryCounter(counter **int64, addition int64) {
	if counter == nil || addition < 0 {
		return
	}
	value := int64(0)
	if *counter != nil {
		value = **counter
	}
	if addition > int64(^uint64(0)>>1)-value {
		return
	}
	value += addition
	*counter = &value
}
