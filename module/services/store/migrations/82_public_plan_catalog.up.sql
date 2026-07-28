ALTER TABLE plans
    ADD COLUMN description TEXT NOT NULL DEFAULT 'Development fixture plan. Replace before launch.',
    ADD COLUMN amount_minor BIGINT,
    ADD COLUMN billing_interval TEXT NOT NULL DEFAULT 'month',
    ADD COLUMN public_visible BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN contact_sales BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN fixture BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD CONSTRAINT plans_amount_minor_check
        CHECK (amount_minor IS NULL OR amount_minor >= 0),
    ADD CONSTRAINT plans_billing_interval_check
        CHECK (billing_interval IN ('month', 'year', 'one_time', 'contact')),
    ADD CONSTRAINT plans_public_price_check
        CHECK (NOT public_visible OR amount_minor IS NOT NULL OR contact_sales);
