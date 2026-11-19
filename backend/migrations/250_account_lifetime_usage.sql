-- Durable per-account account-billed usage.  This survives usage_logs retention cleanup.
CREATE TABLE IF NOT EXISTS account_lifetime_usage (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    total_account_cost NUMERIC(38, 18) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION update_account_lifetime_usage()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.account_id IS NOT NULL THEN
        INSERT INTO account_lifetime_usage (account_id, total_account_cost, updated_at)
        VALUES (
            NEW.account_id,
            COALESCE(NEW.account_stats_cost, NEW.total_cost) * COALESCE(NEW.account_rate_multiplier, 1),
            NOW()
        )
        ON CONFLICT (account_id) DO UPDATE
        SET total_account_cost = account_lifetime_usage.total_account_cost + EXCLUDED.total_account_cost,
            updated_at = EXCLUDED.updated_at;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS usage_logs_account_lifetime_usage ON usage_logs;
CREATE TRIGGER usage_logs_account_lifetime_usage
AFTER INSERT ON usage_logs
FOR EACH ROW
EXECUTE FUNCTION update_account_lifetime_usage();

-- Install the trigger before the backfill. CREATE TRIGGER takes a
-- ShareRowExclusive lock on usage_logs, so once this statement acquires its
-- lock, concurrent inserts wait until the migration commits. That prevents a
-- row committed after the backfill snapshot from being missed while the
-- trigger is not yet active.
-- Existing rows must be included when this migration is first installed.
INSERT INTO account_lifetime_usage (account_id, total_account_cost, updated_at)
SELECT account_id,
       COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0),
       NOW()
FROM usage_logs
WHERE account_id IS NOT NULL
GROUP BY account_id
ON CONFLICT (account_id) DO UPDATE
SET total_account_cost = EXCLUDED.total_account_cost,
    updated_at = EXCLUDED.updated_at;
