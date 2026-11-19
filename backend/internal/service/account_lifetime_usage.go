package service

import (
	"context"
	"fmt"
	"time"
)

// AccountLifetimeUsage is the durable account-billed total.  UpdatedAt is nil
// when the account has no persisted usage yet.
type AccountLifetimeUsage struct {
	TotalAccountCost float64    `json:"total_account_cost"`
	UpdatedAt        *time.Time `json:"updated_at"`
}

type accountLifetimeUsageReader interface {
	GetAccountLifetimeUsage(context.Context, int64) (*AccountLifetimeUsage, error)
}

// GetAccountLifetimeUsage returns the retained total for one account.  The
// optional interface keeps existing test repositories and integrations source
// compatible while the concrete usage-log repository provides the storage.
func (s *AccountUsageService) GetAccountLifetimeUsage(ctx context.Context, accountID int64, action string) (*AccountLifetimeUsage, error) {
	if action != "accounts.stats.read" && action != "accounts.usage.read" {
		action = "accounts.stats.read"
	}
	if err := scopedAccountIDAccess(ctx, s.accountRepo, accountID, action, action == "accounts.usage.read"); err != nil {
		return nil, err
	}
	reader, ok := s.usageLogRepo.(accountLifetimeUsageReader)
	if !ok {
		return nil, fmt.Errorf("account lifetime usage is unavailable")
	}
	return reader.GetAccountLifetimeUsage(ctx, accountID)
}
