ALTER TABLE plans
    DROP CONSTRAINT IF EXISTS plans_public_price_check,
    DROP CONSTRAINT IF EXISTS plans_billing_interval_check,
    DROP CONSTRAINT IF EXISTS plans_amount_minor_check,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS fixture,
    DROP COLUMN IF EXISTS contact_sales,
    DROP COLUMN IF EXISTS public_visible,
    DROP COLUMN IF EXISTS billing_interval,
    DROP COLUMN IF EXISTS amount_minor,
    DROP COLUMN IF EXISTS description;
