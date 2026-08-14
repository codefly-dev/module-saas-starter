-- Reference-catalog rows can only be removed while no identity still points at
-- them; the foreign key blocks the delete otherwise, which is the correct
-- refusal to strand user_identities rows.
DELETE FROM identity_providers
WHERE provider_id IN ('oidc', 'auth0', 'okta', 'ping');
