ALTER TABLE sessions DROP COLUMN IF EXISTS mfa_satisfied;
DROP TABLE IF EXISTS mfa_login_transactions;
