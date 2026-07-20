DROP TABLE IF EXISTS webauthn_ceremonies;
DROP TABLE IF EXISTS webauthn_credentials;

DELETE FROM mfa_devices WHERE device_type = 'webauthn';
ALTER TABLE mfa_devices DROP CONSTRAINT IF EXISTS mfa_devices_secret_by_type;
ALTER TABLE mfa_devices ALTER COLUMN secret_encrypted SET NOT NULL;
