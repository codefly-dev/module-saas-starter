-- The hop's admission keys (codefly.dev/authority@0.3.3, RegisterAdmissionKey).
--
-- The composition's hop signs each call it admits, and every module behind it
-- verifies the admission by the public key accounts publishes in Keys. The hop
-- registers that public key under its workload credential and renews it while
-- it runs; a registration lapses at expires_at unless renewed, so a key no
-- running hop signs with is withdrawn without being named.
--
-- A row, not process memory: accounts runs several replicas behind one Keys
-- read, and the hop renews through whichever one it reaches. Every lease is
-- judged by the database's clock, so every replica publishes the same set.
--
-- Only public keys live here. The seed never leaves the hop.

CREATE TABLE public.hop_admission_keys (
    -- stamp.KeyID of the key: the id the hop signs under, derived from it.
    key_id text NOT NULL,
    -- The raw Ed25519 public key.
    public_key bytea NOT NULL,
    registered_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    -- When the registration lapses unless the hop registers the key again.
    expires_at timestamp with time zone NOT NULL,

    CONSTRAINT hop_admission_keys_public_key_check CHECK ((octet_length(public_key) = 32)),
    CONSTRAINT hop_admission_keys_lease_check CHECK ((expires_at > registered_at))
);

ALTER TABLE ONLY public.hop_admission_keys
    ADD CONSTRAINT hop_admission_keys_pkey PRIMARY KEY (key_id);

-- Control-plane only, like every other platform relation: no tenant column, no
-- row-level security, and request traffic has no access. DELETE removes a
-- lapsed registration, which publishes nothing either way.
GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE public.hop_admission_keys TO app_control_plane;
