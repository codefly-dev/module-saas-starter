-- Which key seals an organization's stored credentials (#1013).
--
-- Default: none. An organization with no row here is sealed under the
-- deployment's own key, which is the "the customer does not care" case and stays
-- the default forever. A row is an OVERRIDE: this organization's credentials are
-- sealed under the key it names instead.
--
-- key_ref is OPAQUE to this host. It is whatever the key service the deployment
-- binds calls a key, and accounts hands it to that service without parsing it:
-- a Vault transit key name, a cloud KMS resource name, anything a later backend
-- uses. Parsing it here would put one vendor's grammar in the database, which is
-- the mistake the key-service seam exists to avoid (codefly-dev/cli#924).
--
-- accounts never PROVISIONS a key. The platform creates it and records the
-- binding; accounts reads the binding and refuses an organization whose key it
-- cannot reach. Same contract as the signing key's custody: the cell provisions,
-- this host reads.
CREATE TABLE public.org_key_bindings (
 org_id uuid PRIMARY KEY REFERENCES public.organizations(id) ON DELETE CASCADE,
 key_ref text NOT NULL CHECK (length(btrim(key_ref)) BETWEEN 1 AND 512),
 -- Whether the key is held by the customer rather than by this deployment.
 -- The re-seal sweep REFUSES such an organization: re-sealing its credentials
 -- under a key this deployment controls would undo the only property a
 -- customer-held key provides, and it would do so while reporting success.
 customer_held boolean NOT NULL DEFAULT false,
 bound_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 -- Set when the key is gone: destroyed by this deployment on request, or
 -- revoked by the customer. Every credential sealed under it is unreadable from
 -- that moment, which is the point.
 --
 -- It is recorded rather than inferred because the two indistinguishable
 -- failures have opposite meanings. Without this column a deliberately
 -- destroyed key is just a key service answering "cannot decrypt", which this
 -- host already classifies as a credential the user should re-enter — so
 -- crypto-shredding would tell every affected customer to reconnect their
 -- sources, for data that was destroyed on their own instruction.
 revoked_at timestamptz,
 revoked_reason text CHECK (revoked_reason IS NULL OR length(btrim(revoked_reason)) BETWEEN 1 AND 512),
 CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);
ALTER TABLE public.org_key_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.org_key_bindings FORCE ROW LEVEL SECURITY;

-- A tenant READS its own binding and may not write one: which key seals an
-- organization's data is the platform's provisioning, not a tenant-editable
-- setting. A tenant that could write one could point its own credentials at
-- another organization's key.
CREATE POLICY org_key_bindings_tenant ON public.org_key_bindings
 FOR SELECT TO app_tenant
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT ON public.org_key_bindings TO app_tenant;

CREATE POLICY app_control_plane_explicit_rows ON public.org_key_bindings TO app_control_plane
 USING (CURRENT_USER = 'app_control_plane'::name)
 WITH CHECK (CURRENT_USER = 'app_control_plane'::name);
GRANT SELECT, INSERT, DELETE, UPDATE ON public.org_key_bindings TO app_control_plane;

-- The delivery paths open a sealed credential outside any tenant transaction, so
-- they have to resolve the organization's key too. webhook_subscriptions grants
-- SELECT to both of these for exactly the same reason; a binding readable by
-- fewer roles than the secret it unlocks fails at delivery time, not at boot.
CREATE POLICY app_job_worker_explicit_rows ON public.org_key_bindings TO app_job_worker
 USING (CURRENT_USER = 'app_job_worker'::name)
 WITH CHECK (CURRENT_USER = 'app_job_worker'::name);
GRANT SELECT ON public.org_key_bindings TO app_job_worker;

CREATE POLICY app_webhook_worker_explicit_rows ON public.org_key_bindings TO app_webhook_worker
 USING (CURRENT_USER = 'app_webhook_worker'::name)
 WITH CHECK (CURRENT_USER = 'app_webhook_worker'::name);
GRANT SELECT ON public.org_key_bindings TO app_webhook_worker;

-- A revocation is terminal. Un-revoking would assert that credentials sealed
-- under a destroyed key are readable again, which no edit to this row can make
-- true, and it would let a holder of the application login quietly undo a
-- customer's instruction to destroy their data.
CREATE FUNCTION public.org_key_binding_revocation_is_terminal() RETURNS trigger LANGUAGE plpgsql
 SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
 IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
  RAISE EXCEPTION 'org key binding revocation is terminal';
 END IF;
 -- The key a binding names may not change either: every stored envelope records
 -- the key that sealed it, so repointing the row would not re-seal anything and
 -- would leave the binding describing a key that sealed none of the data.
 IF NEW.key_ref IS DISTINCT FROM OLD.key_ref THEN
  RAISE EXCEPTION 'org key binding key_ref is immutable; re-seal under a new binding instead';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER org_key_binding_revocation_is_terminal BEFORE UPDATE ON public.org_key_bindings
 FOR EACH ROW EXECUTE FUNCTION public.org_key_binding_revocation_is_terminal();
REVOKE ALL ON FUNCTION public.org_key_binding_revocation_is_terminal() FROM PUBLIC;
