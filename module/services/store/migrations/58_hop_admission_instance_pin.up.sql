-- Interface 0.3.4 (codefly-dev/authority#32): one admission key is pinned per
-- hop instance, and every registration carries a proof used once.
--
-- instance is the hop instance (its pod) whose registration holds the key: the
-- first registration for an instance binds its key until the lease lapses, the
-- same key renews it, and another key for that instance is refused until then.
-- A key published before this migration names no instance and lapses with its
-- lease.
ALTER TABLE public.hop_admission_keys ADD COLUMN instance text;
ALTER TABLE public.hop_admission_keys
    ADD CONSTRAINT hop_admission_keys_instance_check CHECK ((instance IS NULL OR (length(instance) BETWEEN 1 AND 253)));
CREATE UNIQUE INDEX hop_admission_keys_instance_key ON public.hop_admission_keys USING btree (instance) WHERE (instance IS NOT NULL);

-- The registration proofs' nonces, each kept until a proof carrying it could no
-- longer be judged fresh: a nonce seen once is refused again until then.
CREATE TABLE public.hop_admission_proofs (
    nonce text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT hop_admission_proofs_pkey PRIMARY KEY (nonce),
    CONSTRAINT hop_admission_proofs_nonce_check CHECK ((nonce ~ '^[0-9a-f]{64}$'))
);

GRANT SELECT,INSERT,DELETE ON TABLE public.hop_admission_proofs TO app_control_plane;
