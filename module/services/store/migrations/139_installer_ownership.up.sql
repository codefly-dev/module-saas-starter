-- Durable ownership separates installer-managed rows from human installs.
ALTER TABLE public.installations ADD COLUMN installer_principal_id uuid;
