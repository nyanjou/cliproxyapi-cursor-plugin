package provider

import (
	"context"
	"strings"
)

type CursorQuotaSnapshot struct {
	Provider                string               `json:"provider"`
	Account                 string               `json:"account,omitempty"`
	Email                   string               `json:"email,omitempty"`
	Tier                    string               `json:"tier,omitempty"`
	Version                 string               `json:"version,omitempty"`
	StatusKnown             bool                 `json:"status_known"`
	Authenticated           bool                 `json:"authenticated"`
	RemainingQuotaAvailable bool                 `json:"remaining_quota_available"`
	RemainingQuotaMessage   string               `json:"remaining_quota_message"`
	Usage                   GatewayUsageSnapshot `json:"usage"`
}

func (s *Service) CursorQuota(ctx context.Context) (CursorQuotaSnapshot, error) {
	storage, err := s.statusStorage(ctx)
	if err != nil {
		return CursorQuotaSnapshot{}, err
	}
	return CursorQuotaSnapshot{
		Provider:                providerID,
		Account:                 strings.TrimSpace(storage.Account),
		Email:                   strings.TrimSpace(storage.Email),
		Tier:                    strings.TrimSpace(storage.Tier),
		Version:                 strings.TrimSpace(storage.Version),
		StatusKnown:             storage.StatusKnown,
		Authenticated:           storage.Authenticated,
		RemainingQuotaAvailable: false,
		RemainingQuotaMessage:   "Official Cursor Agent CLI does not expose numeric remaining subscription quota.",
		Usage:                   s.GatewayUsage(),
	}, nil
}
