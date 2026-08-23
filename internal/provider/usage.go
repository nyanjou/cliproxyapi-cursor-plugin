package provider

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type usageAggregate struct {
	Requests            int
	Failures            int
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	LastUsed            time.Time
}

type GatewayUsageSnapshot struct {
	ObservedSince time.Time           `json:"observed_since,omitempty"`
	Requests      int                 `json:"requests"`
	Failures      int                 `json:"failures"`
	Totals        GatewayUsageTotals  `json:"totals"`
	Models        []GatewayUsageModel `json:"models"`
}

type GatewayUsageTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type GatewayUsageModel struct {
	Model               string    `json:"model"`
	Requests            int       `json:"requests"`
	Failures            int       `json:"failures"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	ReasoningTokens     int64     `json:"reasoning_tokens"`
	CachedTokens        int64     `json:"cached_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	TotalTokens         int64     `json:"total_tokens"`
	LastUsed            time.Time `json:"last_used,omitempty"`
}

func (s *Service) HandleUsage(_ context.Context, record pluginapi.UsageRecord) {
	s.RecordUsage(record)
}

func (s *Service) RecordUsage(record pluginapi.UsageRecord) {
	if !strings.EqualFold(strings.TrimSpace(record.Provider), providerID) {
		return
	}
	model := strings.TrimSpace(firstNonEmpty(record.Model, record.Alias))
	if model == "" {
		return
	}
	usedAt := record.RequestedAt.UTC()
	if usedAt.IsZero() {
		usedAt = s.now().UTC()
	}

	s.usageMu.Lock()
	defer s.usageMu.Unlock()

	if s.usageStarted.IsZero() || usedAt.Before(s.usageStarted) {
		s.usageStarted = usedAt
	}
	entry := s.usageByModel[model]
	if entry == nil {
		entry = &usageAggregate{}
		s.usageByModel[model] = entry
	}
	entry.Requests++
	if record.Failed {
		entry.Failures++
	}
	entry.InputTokens += record.Detail.InputTokens
	entry.OutputTokens += record.Detail.OutputTokens
	entry.ReasoningTokens += record.Detail.ReasoningTokens
	entry.CachedTokens += record.Detail.CachedTokens
	entry.CacheReadTokens += record.Detail.CacheReadTokens
	entry.CacheCreationTokens += record.Detail.CacheCreationTokens
	entry.TotalTokens += normalizedTotalTokens(record.Detail)
	if entry.LastUsed.IsZero() || usedAt.After(entry.LastUsed) {
		entry.LastUsed = usedAt
	}
}

func (s *Service) GatewayUsage() GatewayUsageSnapshot {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()

	snapshot := GatewayUsageSnapshot{ObservedSince: s.usageStarted}
	for model, entry := range s.usageByModel {
		row := GatewayUsageModel{
			Model:               model,
			Requests:            entry.Requests,
			Failures:            entry.Failures,
			InputTokens:         entry.InputTokens,
			OutputTokens:        entry.OutputTokens,
			ReasoningTokens:     entry.ReasoningTokens,
			CachedTokens:        entry.CachedTokens,
			CacheReadTokens:     entry.CacheReadTokens,
			CacheCreationTokens: entry.CacheCreationTokens,
			TotalTokens:         entry.TotalTokens,
			LastUsed:            entry.LastUsed,
		}
		snapshot.Models = append(snapshot.Models, row)
		snapshot.Requests += row.Requests
		snapshot.Failures += row.Failures
		snapshot.Totals.InputTokens += row.InputTokens
		snapshot.Totals.OutputTokens += row.OutputTokens
		snapshot.Totals.ReasoningTokens += row.ReasoningTokens
		snapshot.Totals.CachedTokens += row.CachedTokens
		snapshot.Totals.CacheReadTokens += row.CacheReadTokens
		snapshot.Totals.CacheCreationTokens += row.CacheCreationTokens
		snapshot.Totals.TotalTokens += row.TotalTokens
	}
	sort.Slice(snapshot.Models, func(i, j int) bool {
		if !snapshot.Models[i].LastUsed.Equal(snapshot.Models[j].LastUsed) {
			return snapshot.Models[i].LastUsed.After(snapshot.Models[j].LastUsed)
		}
		return strings.ToLower(snapshot.Models[i].Model) < strings.ToLower(snapshot.Models[j].Model)
	})
	return snapshot
}

func normalizedTotalTokens(detail pluginapi.UsageDetail) int64 {
	if detail.TotalTokens > 0 {
		return detail.TotalTokens
	}
	total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	if total > 0 {
		return total
	}
	return detail.CacheReadTokens + detail.CacheCreationTokens + detail.CachedTokens
}
