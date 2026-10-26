-- A module's hop through a binding records WHICH binding it went through and
-- under which of that binding's scope sets it was issued (invoke, lookup or
-- headless). The capability seals only its last hop's binding and names no
-- operation at all, so without this a hop upstream in a chain could only be
-- held to the union of its binding's sets: narrowing invoke left a hop issued
-- under invoke standing as long as lookup still granted the same action. With
-- it, each hop is held to the exact set it was issued under, and narrowing that
-- set ends the hop. Both are null for hops that exercise no module binding
-- (an agent's delegated hop, a hop recorded before this column existed).
ALTER TABLE public.actor_chain_journal
    ADD COLUMN operation_binding_id text,
    ADD COLUMN operation text,
    ADD CONSTRAINT actor_chain_journal_operation_check
        CHECK (operation IS NULL OR operation IN ('invoke', 'lookup', 'headless')),
    ADD CONSTRAINT actor_chain_journal_operation_whole
        CHECK ((operation IS NULL) = (operation_binding_id IS NULL));
