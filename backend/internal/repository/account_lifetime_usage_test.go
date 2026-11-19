package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestGetAccountLifetimeUsageReturnsDurableTotal(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	updated := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT total_account_cost, updated_at\s+FROM account_lifetime_usage`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"total_account_cost", "updated_at"}).AddRow(12.345, updated))

	repo := &usageLogRepository{sql: db}
	got, err := repo.GetAccountLifetimeUsage(context.Background(), 42)
	require.NoError(t, err)
	require.InDelta(t, 12.345, got.TotalAccountCost, 0.0000001)
	require.NotNil(t, got.UpdatedAt)
	require.Equal(t, updated, *got.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAccountLifetimeUsageEmptyAccountIsZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectQuery(`SELECT total_account_cost, updated_at\s+FROM account_lifetime_usage`).
		WithArgs(int64(42)).
		WillReturnError(sql.ErrNoRows)

	repo := &usageLogRepository{sql: db}
	got, err := repo.GetAccountLifetimeUsage(context.Background(), 42)
	require.NoError(t, err)
	require.Zero(t, got.TotalAccountCost)
	require.Nil(t, got.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
