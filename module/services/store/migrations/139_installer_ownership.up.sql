-- Durable ownership separates installer-managed rows from human installs.
ALTER TABLE installations ADD COLUMN installer_principal_id uuid;
