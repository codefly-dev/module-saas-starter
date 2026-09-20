-- First-party client sign-in (issue #853).
--
-- A registered client sends the person to the host's own login page. Once they
-- are signed in, the host issues a one-time authorization code here and
-- redirects back to the client, which redeems it for its own tokens. The code
-- is the only thing that crosses the boundary, it is single-use, and it carries
-- no identity of its own: it names the host session that authorized it, so
-- redemption resolves the person's current authorization rather than a snapshot
-- taken before the redirect.

-- The client a session belongs to, projected into the access token's `azp`
-- claim. NULL is the host's own web session, which has no client.
ALTER TABLE public.sessions ADD COLUMN client_id text;

CREATE TABLE public.client_authorization_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code_hash text NOT NULL,
    client_id text NOT NULL,
    -- The redirect URI and PKCE challenge the code was issued against. Both are
    -- re-checked at redemption, so a code delivered to one registered URI
    -- cannot be redeemed as if it had been sent to another.
    redirect_uri text NOT NULL,
    code_challenge text NOT NULL,
    user_id uuid NOT NULL,
    -- The host session that authorized this code. Redemption reads current
    -- authorization through it, so a code outlives neither the sign-in it came
    -- from nor a logout that follows it.
    session_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT client_authorization_codes_code_hash_check CHECK ((length(code_hash) = 64))
);

ALTER TABLE ONLY public.client_authorization_codes FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.client_authorization_codes
    ADD CONSTRAINT client_authorization_codes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.client_authorization_codes
    ADD CONSTRAINT client_authorization_codes_code_hash_key UNIQUE (code_hash);

ALTER TABLE ONLY public.client_authorization_codes
    ADD CONSTRAINT client_authorization_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(uuid) ON DELETE CASCADE;

ALTER TABLE ONLY public.client_authorization_codes
    ADD CONSTRAINT client_authorization_codes_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.sessions(id) ON DELETE CASCADE;

CREATE INDEX idx_client_authorization_codes_expiry ON public.client_authorization_codes USING btree (expires_at) WHERE (consumed_at IS NULL);

ALTER TABLE public.client_authorization_codes ENABLE ROW LEVEL SECURITY;

CREATE POLICY client_authorization_codes_user ON public.client_authorization_codes
    USING (((user_id)::text = current_setting('app.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('app.current_user_id'::text, true)));

CREATE POLICY app_control_plane_explicit_rows ON public.client_authorization_codes
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

-- The issuing path runs as the signed-in person, so app_tenant may only insert
-- its own row. Redemption arrives with nothing but the random code, so it runs
-- on the control plane, which is also what expires abandoned codes.
GRANT INSERT ON TABLE public.client_authorization_codes TO app_tenant;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.client_authorization_codes TO app_control_plane;
