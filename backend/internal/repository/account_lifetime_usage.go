package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetAccountLifetimeUsage reads the durable account-billed total.  It does not
// depend on usage_logs, which are subject to retention cleanup.
func (r *usageLogRepository) GetAccountLifetimeUsage(ctx context.Context, accountID int64) (*service.AccountLifetimeUsage, error) {
	result := &service.AccountLifetimeUsage{}
	var updatedAt sql.NullTime
	err := scanSingleRow(ctx, r.sql, `
		SELECT total_account_cost, updated_at
		FROM account_lifetime_usage
		WHERE account_id = $1
	`, []any{accountID}, &result.TotalAccountCost, &updatedAt)
	if err != nil {
		// A valid account with no billable usage has no row until its first log.
		if errors.Is(err, sql.ErrNoRows) {
			return result, nil
		}
		return nil, err
	}
	if updatedAt.Valid {
		result.UpdatedAt = &updatedAt.Time
	}
	return result, nil
}
