ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS quota_boost_monthly_limit INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS quota_boost_monthly_used INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS quota_boost_period_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS quota_boost_activated_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'user_subscriptions_quota_boost_monthly_limit_check'
          AND conrelid = 'user_subscriptions'::regclass
          AND contype = 'c'
    ) THEN
        ALTER TABLE user_subscriptions
            ADD CONSTRAINT user_subscriptions_quota_boost_monthly_limit_check
            CHECK (quota_boost_monthly_limit BETWEEN 0 AND 31);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'user_subscriptions_quota_boost_monthly_used_check'
          AND conrelid = 'user_subscriptions'::regclass
          AND contype = 'c'
    ) THEN
        ALTER TABLE user_subscriptions
            ADD CONSTRAINT user_subscriptions_quota_boost_monthly_used_check
            CHECK (quota_boost_monthly_used >= 0);
    END IF;
END $$;
