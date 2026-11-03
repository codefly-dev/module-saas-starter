-- The caller's organisation and user are bound, and read back, under the
-- names the platform's Postgres library installs: codefly.current_tenant_id
-- and codefly.current_user_id, not this host's own app.current_org_id and
-- app.current_user_id.
--
-- The library (codefly-dev/service-postgres, libs/go/store.go) defaults its
-- scoped transaction to those two settings, and that is what the other stores
-- on this platform read. The host carried names of its own, so an operator
-- reading two of these databases' policies read two conventions. This is that
-- rename and nothing else: every policy keeps its USING and WITH CHECK shape,
-- its PERMISSIVE/RESTRICTIVE kind, its verb and its role list; every table
-- keeps ENABLE and FORCE ROW LEVEL SECURITY; the roles stay NOBYPASSRLS; the
-- binding stays transaction-local and fail-closed, so an unset setting still
-- reads as no rows rather than all of them. A table with no policy -- the
-- platform-global ones, whose boundary is the grant -- is untouched.
--
-- Generated from pg_policy and pg_proc rather than written by hand, because
-- the catalog is the only place that knows the whole set. At version 64 it
-- was 75 policies over 70 relations, 6 more on partitions of
-- public.audit_events, and 9 function bodies. Each expression below is the
-- catalog's own rendering under an empty search_path -- which is how the
-- baseline's text was produced, and what makes it schema-qualified -- with
-- the two setting names substituted and nothing else touched.

-- The policies.

DROP POLICY actor_chain_journal_tenant ON public.actor_chain_journal;
CREATE POLICY actor_chain_journal_tenant ON public.actor_chain_journal
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY actor_chain_revocations_tenant ON public.actor_chain_revocations;
CREATE POLICY actor_chain_revocations_tenant ON public.actor_chain_revocations
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY api_keys_tenant ON public.api_keys;
CREATE POLICY api_keys_tenant ON public.api_keys
    FOR ALL
    TO PUBLIC
    USING (((organization_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((organization_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY approval_decisions_tenant ON public.approval_decisions;
CREATE POLICY approval_decisions_tenant ON public.approval_decisions
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY approval_requests_tenant ON public.approval_requests;
CREATE POLICY approval_requests_tenant ON public.approval_requests
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY audit_event_idempotency_tenant ON public.audit_event_idempotency;
CREATE POLICY audit_event_idempotency_tenant ON public.audit_event_idempotency
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY audit_events_tenant ON public.audit_events;
CREATE POLICY audit_events_tenant ON public.audit_events
    FOR ALL
    TO PUBLIC
    USING (((org_id IS NOT NULL) AND ((org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))
    WITH CHECK ((((org_id IS NOT NULL) AND ((org_id)::text = current_setting('codefly.current_tenant_id'::text, true))) OR ((org_id IS NULL) AND (actor_id IS NOT NULL) AND ((actor_id)::text = current_setting('codefly.current_user_id'::text, true)))));

DROP POLICY client_authorization_codes_user ON public.client_authorization_codes;
CREATE POLICY client_authorization_codes_user ON public.client_authorization_codes
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY codefly_effect_receipts_tenant ON public.codefly_effect_receipts;
CREATE POLICY codefly_effect_receipts_tenant ON public.codefly_effect_receipts
    FOR ALL
    TO app_tenant
    USING ((tenant = NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text)))
    WITH CHECK ((tenant = NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text)));

DROP POLICY connector_credentials_tenant ON public.connector_credentials;
CREATE POLICY connector_credentials_tenant ON public.connector_credentials
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY dashboards_tenant ON public.dashboards;
CREATE POLICY dashboards_tenant ON public.dashboards
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY datasource_account_links_tenant ON public.datasource_account_links;
CREATE POLICY datasource_account_links_tenant ON public.datasource_account_links
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_domains_tenant ON public.datasource_domains;
CREATE POLICY datasource_domains_tenant ON public.datasource_domains
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_group_bindings_tenant ON public.datasource_group_bindings;
CREATE POLICY datasource_group_bindings_tenant ON public.datasource_group_bindings
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_oauth_consumed_states_tenant ON public.datasource_oauth_consumed_states;
CREATE POLICY datasource_oauth_consumed_states_tenant ON public.datasource_oauth_consumed_states
    FOR ALL
    TO app_tenant
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_object_manifests_tenant ON public.datasource_object_manifests;
CREATE POLICY datasource_object_manifests_tenant ON public.datasource_object_manifests
    FOR ALL
    TO app_tenant
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_operation_attempts_tenant ON public.datasource_operation_attempts;
CREATE POLICY datasource_operation_attempts_tenant ON public.datasource_operation_attempts
    FOR ALL
    TO app_tenant
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_source_operations_tenant ON public.datasource_source_operations;
CREATE POLICY datasource_source_operations_tenant ON public.datasource_source_operations
    FOR ALL
    TO app_tenant
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY datasource_sources_tenant ON public.datasource_sources;
CREATE POLICY datasource_sources_tenant ON public.datasource_sources
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY delegation_grants_tenant ON public.delegation_grants;
CREATE POLICY delegation_grants_tenant ON public.delegation_grants
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY domain_events_tenant ON public.domain_events;
CREATE POLICY domain_events_tenant ON public.domain_events
    FOR SELECT
    TO PUBLIC
    USING (((tenant_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY entitlement_overrides_tenant ON public.entitlement_overrides;
CREATE POLICY entitlement_overrides_tenant ON public.entitlement_overrides
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY executable_artifact_approvals_tenant ON public.executable_artifact_approvals;
CREATE POLICY executable_artifact_approvals_tenant ON public.executable_artifact_approvals
    FOR ALL
    TO app_tenant
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY gdpr_requests_user ON public.gdpr_requests;
CREATE POLICY gdpr_requests_user ON public.gdpr_requests
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY github_app_installations_tenant ON public.github_app_installations;
CREATE POLICY github_app_installations_tenant ON public.github_app_installations
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY github_app_setups_tenant ON public.github_app_setups;
CREATE POLICY github_app_setups_tenant ON public.github_app_setups
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY installations_tenant ON public.installations;
CREATE POLICY installations_tenant ON public.installations
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY invitations_tenant ON public.invitations;
CREATE POLICY invitations_tenant ON public.invitations
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY job_messages_request_enqueue ON public.job_messages;
CREATE POLICY job_messages_request_enqueue ON public.job_messages
    FOR INSERT
    TO app_tenant
    WITH CHECK (((direction = 'outbox'::text) AND (replay_of IS NULL) AND (((scope_kind = 'tenant'::text) AND (organization_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid)) OR ((scope_kind = 'subject'::text) AND (subject_id = (NULLIF(current_setting('codefly.current_user_id'::text, true), ''::text))::uuid)))));

DROP POLICY membership_integrity_findings_tenant ON public.membership_integrity_findings;
CREATE POLICY membership_integrity_findings_tenant ON public.membership_integrity_findings
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY mfa_backup_codes_user ON public.mfa_backup_codes;
CREATE POLICY mfa_backup_codes_user ON public.mfa_backup_codes
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY mfa_devices_user ON public.mfa_devices;
CREATE POLICY mfa_devices_user ON public.mfa_devices
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY mfa_login_transactions_user ON public.mfa_login_transactions;
CREATE POLICY mfa_login_transactions_user ON public.mfa_login_transactions
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY notifications_user ON public.notifications;
CREATE POLICY notifications_user ON public.notifications
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY onboarding_progress_user ON public.onboarding_progress;
CREATE POLICY onboarding_progress_user ON public.onboarding_progress
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY org_generic_settings_tenant ON public.org_generic_settings;
CREATE POLICY org_generic_settings_tenant ON public.org_generic_settings
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY org_identity_providers_tenant ON public.org_identity_providers;
CREATE POLICY org_identity_providers_tenant ON public.org_identity_providers
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY org_key_bindings_tenant ON public.org_key_bindings;
CREATE POLICY org_key_bindings_tenant ON public.org_key_bindings
    FOR SELECT
    TO app_tenant
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY org_settings_tenant ON public.org_settings;
CREATE POLICY org_settings_tenant ON public.org_settings
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY organization_activations_tenant ON public.organization_activations;
CREATE POLICY organization_activations_tenant ON public.organization_activations
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY organization_authorization_revisions_tenant_read ON public.organization_authorization_revisions;
CREATE POLICY organization_authorization_revisions_tenant_read ON public.organization_authorization_revisions
    FOR SELECT
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY organization_members_tenant ON public.organization_members;
CREATE POLICY organization_members_tenant ON public.organization_members
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY organizations_self ON public.organizations;
CREATE POLICY organizations_self ON public.organizations
    FOR ALL
    TO PUBLIC
    USING (((id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY principal_authorization_revisions_tenant_read ON public.principal_authorization_revisions;
CREATE POLICY principal_authorization_revisions_tenant_read ON public.principal_authorization_revisions
    FOR SELECT
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY principals_access ON public.principals;
CREATE POLICY principals_access ON public.principals
    FOR ALL
    TO PUBLIC
    USING ((((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)) OR ((id)::text = current_setting('codefly.current_user_id'::text, true)) OR (EXISTS ( SELECT 1
   FROM public.organization_members om
  WHERE ((om.user_id = principals.id) AND ((om.org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))))))
    WITH CHECK ((((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)) OR ((id)::text = current_setting('codefly.current_user_id'::text, true))));

DROP POLICY record_shares_tenant ON public.record_shares;
CREATE POLICY record_shares_tenant ON public.record_shares
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY resource_follows_user ON public.resource_follows;
CREATE POLICY resource_follows_user ON public.resource_follows
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY role_assignments_polymorphic ON public.role_assignments;
CREATE POLICY role_assignments_polymorphic ON public.role_assignments
    FOR ALL
    TO PUBLIC
    USING (((org_id IS NULL) OR ((org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))
    WITH CHECK (((org_id IS NOT NULL) AND ((org_id)::text = current_setting('codefly.current_tenant_id'::text, true))));

DROP POLICY role_permissions_delete ON public.role_permissions;
CREATE POLICY role_permissions_delete ON public.role_permissions
    FOR DELETE
    TO PUBLIC
    USING ((EXISTS ( SELECT 1
   FROM public.roles
  WHERE ((roles.id = role_permissions.role_id) AND (roles.org_id IS NOT NULL) AND ((roles.org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))));

DROP POLICY role_permissions_insert ON public.role_permissions;
CREATE POLICY role_permissions_insert ON public.role_permissions
    FOR INSERT
    TO PUBLIC
    WITH CHECK ((EXISTS ( SELECT 1
   FROM public.roles
  WHERE ((roles.id = role_permissions.role_id) AND (roles.org_id IS NOT NULL) AND ((roles.org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))));

DROP POLICY role_permissions_select ON public.role_permissions;
CREATE POLICY role_permissions_select ON public.role_permissions
    FOR SELECT
    TO PUBLIC
    USING ((EXISTS ( SELECT 1
   FROM public.roles
  WHERE ((roles.id = role_permissions.role_id) AND ((roles.org_id IS NULL) OR ((roles.org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))))));

DROP POLICY roles_polymorphic ON public.roles;
CREATE POLICY roles_polymorphic ON public.roles
    FOR ALL
    TO PUBLIC
    USING (((org_id IS NULL) OR ((org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))
    WITH CHECK (((org_id IS NOT NULL) AND ((org_id)::text = current_setting('codefly.current_tenant_id'::text, true))));

DROP POLICY scope_grants_tenant ON public.scope_grants;
CREATE POLICY scope_grants_tenant ON public.scope_grants
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY scope_nodes_tenant ON public.scope_nodes;
CREATE POLICY scope_nodes_tenant ON public.scope_nodes
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY sessions_user ON public.sessions;
CREATE POLICY sessions_user ON public.sessions
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY source_delegations_tenant ON public.source_delegations;
CREATE POLICY source_delegations_tenant ON public.source_delegations
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY source_read_revisions_tenant ON public.source_read_revisions;
CREATE POLICY source_read_revisions_tenant ON public.source_read_revisions
    FOR ALL
    TO PUBLIC
    USING ((org_id = (NULLIF(current_setting('codefly.current_tenant_id'::text, true), ''::text))::uuid));

DROP POLICY subscriptions_tenant ON public.subscriptions;
CREATE POLICY subscriptions_tenant ON public.subscriptions
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY team_members_tenant ON public.team_members;
CREATE POLICY team_members_tenant ON public.team_members
    FOR ALL
    TO PUBLIC
    USING ((EXISTS ( SELECT 1
   FROM public.teams t
  WHERE ((t.id = team_members.team_id) AND ((t.org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))))
    WITH CHECK ((EXISTS ( SELECT 1
   FROM public.teams t
  WHERE ((t.id = team_members.team_id) AND ((t.org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))));

DROP POLICY team_membership_quarantine_tenant ON public.team_membership_quarantine;
CREATE POLICY team_membership_quarantine_tenant ON public.team_membership_quarantine
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY teams_tenant ON public.teams;
CREATE POLICY teams_tenant ON public.teams
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY usage_events_tenant ON public.usage_events;
CREATE POLICY usage_events_tenant ON public.usage_events
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY usage_totals_tenant ON public.usage_totals;
CREATE POLICY usage_totals_tenant ON public.usage_totals
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY user_consent_events_user ON public.user_consent_events;
CREATE POLICY user_consent_events_user ON public.user_consent_events
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY user_consent_preferences_user ON public.user_consent_preferences;
CREATE POLICY user_consent_preferences_user ON public.user_consent_preferences
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY user_identities_user ON public.user_identities;
CREATE POLICY user_identities_user ON public.user_identities
    FOR ALL
    TO PUBLIC
    USING (((user_uuid)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_uuid)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY users_delete ON public.users;
CREATE POLICY users_delete ON public.users
    FOR DELETE
    TO PUBLIC
    USING (((uuid)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY users_insert ON public.users;
CREATE POLICY users_insert ON public.users
    FOR INSERT
    TO PUBLIC
    WITH CHECK (((uuid)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY users_select ON public.users;
CREATE POLICY users_select ON public.users
    FOR SELECT
    TO PUBLIC
    USING (((uuid)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY users_update ON public.users;
CREATE POLICY users_update ON public.users
    FOR UPDATE
    TO PUBLIC
    USING (((uuid)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((uuid)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY webauthn_ceremonies_user ON public.webauthn_ceremonies;
CREATE POLICY webauthn_ceremonies_user ON public.webauthn_ceremonies
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY webauthn_credentials_user ON public.webauthn_credentials;
CREATE POLICY webauthn_credentials_user ON public.webauthn_credentials
    FOR ALL
    TO PUBLIC
    USING (((user_id)::text = current_setting('codefly.current_user_id'::text, true)))
    WITH CHECK (((user_id)::text = current_setting('codefly.current_user_id'::text, true)));

DROP POLICY webhook_deliveries_tenant ON public.webhook_deliveries;
CREATE POLICY webhook_deliveries_tenant ON public.webhook_deliveries
    FOR ALL
    TO PUBLIC
    USING ((EXISTS ( SELECT 1
   FROM public.webhook_subscriptions ws
  WHERE ((ws.id = webhook_deliveries.subscription_id) AND ((ws.org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))))
    WITH CHECK ((EXISTS ( SELECT 1
   FROM public.webhook_subscriptions ws
  WHERE ((ws.id = webhook_deliveries.subscription_id) AND ((ws.org_id)::text = current_setting('codefly.current_tenant_id'::text, true))))));

DROP POLICY webhook_subscriptions_tenant ON public.webhook_subscriptions;
CREATE POLICY webhook_subscriptions_tenant ON public.webhook_subscriptions
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

DROP POLICY work_context_replay_tenant ON public.work_context_replay;
CREATE POLICY work_context_replay_tenant ON public.work_context_replay
    FOR ALL
    TO PUBLIC
    USING (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)))
    WITH CHECK (((org_id)::text = current_setting('codefly.current_tenant_id'::text, true)));

-- The function bodies that read the settings directly.

CREATE OR REPLACE FUNCTION public.enqueue_job_message(p_direction text, p_scope_kind text, p_organization_id uuid, p_subject_id uuid, p_queue text, p_topic text, p_source text, p_idempotency_key text, p_ordering_key text, p_schema_version integer, p_payload bytea, p_content_type text, p_attributes jsonb, p_priority smallint, p_max_attempts integer, p_available_at timestamp with time zone, p_request_fingerprint bytea)
 RETURNS TABLE(job_id uuid, stored_fingerprint bytea, inserted boolean)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    caller_role TEXT;
    inserted_id UUID;
BEGIN
    caller_role := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );

    IF caller_role = 'app_tenant' THEN
        IF p_direction <> 'outbox' THEN
            RAISE EXCEPTION 'request traffic may enqueue outbox work only'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
        IF (
            (
                p_scope_kind = 'tenant'
                AND p_organization_id = NULLIF(
                    current_setting('codefly.current_tenant_id', true), ''
                )::uuid
                AND p_subject_id IS NULL
            )
            OR (
                p_scope_kind = 'subject'
                AND p_subject_id = NULLIF(
                    current_setting('codefly.current_user_id', true), ''
                )::uuid
                AND p_organization_id IS NULL
            )
        ) IS NOT TRUE THEN
            RAISE EXCEPTION 'job scope does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role = 'app_control_plane' THEN
        IF p_direction <> 'outbox'
           OR p_scope_kind <> 'global'
           OR p_organization_id IS NOT NULL
           OR p_subject_id IS NOT NULL THEN
            RAISE EXCEPTION 'control-plane traffic may enqueue global outbox work only'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role <> 'app_job_worker' THEN
        RAISE EXCEPTION 'role % cannot enqueue jobs', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    INSERT INTO public.job_messages (
        direction,
        scope_kind,
        organization_id,
        subject_id,
        queue,
        topic,
        source,
        idempotency_key,
        request_fingerprint,
        ordering_key,
        schema_version,
        payload,
        content_type,
        attributes,
        priority,
        max_attempts,
        available_at
    ) VALUES (
        p_direction,
        p_scope_kind,
        p_organization_id,
        p_subject_id,
        p_queue,
        p_topic,
        p_source,
        p_idempotency_key,
        p_request_fingerprint,
        p_ordering_key,
        p_schema_version,
        p_payload,
        p_content_type,
        p_attributes,
        p_priority,
        p_max_attempts,
        COALESCE(p_available_at, CURRENT_TIMESTAMP)
    )
    ON CONFLICT DO NOTHING
    RETURNING id INTO inserted_id;

    IF inserted_id IS NOT NULL THEN
        RETURN QUERY SELECT inserted_id, p_request_fingerprint, TRUE;
        RETURN;
    END IF;

    RETURN QUERY
    SELECT message.id, message.request_fingerprint, FALSE
    FROM public.job_messages AS message
    WHERE message.direction = p_direction
      AND message.scope_kind = p_scope_kind
      AND message.organization_id IS NOT DISTINCT FROM p_organization_id
      AND message.subject_id IS NOT DISTINCT FROM p_subject_id
      AND message.queue = p_queue
      AND message.source = p_source
      AND message.idempotency_key = p_idempotency_key;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'job idempotency conflict could not be resolved'
            USING ERRCODE = 'serialization_failure';
    END IF;
END
$function$;

CREATE OR REPLACE FUNCTION public.identity_administered_organizations(p_user_id uuid)
 RETURNS TABLE(administered_org_id uuid, eligible_administrators integer, other_active_members integer)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
    SELECT held.org_id,
           (
               SELECT count(*)::integer
               FROM public.organization_members AS peer
               JOIN public.users AS peer_holder
                 ON peer_holder.uuid = peer.user_id
               WHERE peer.org_id = held.org_id
                 AND peer.role IN ('owner', 'admin')
                 AND peer_holder.status = 'active'
           ),
           (
               SELECT count(*)::integer
               FROM public.organization_members AS peer
               JOIN public.users AS peer_holder
                 ON peer_holder.uuid = peer.user_id
               WHERE peer.org_id = held.org_id
                 AND peer.user_id <> p_user_id
                 AND peer_holder.status = 'active'
           )
    FROM public.organization_members AS held
    JOIN public.users AS holder
      ON holder.uuid = held.user_id
    WHERE held.user_id = p_user_id
      AND p_user_id::text = pg_catalog.current_setting('codefly.current_user_id', true)
      AND held.role IN ('owner', 'admin')
      AND holder.status = 'active'
    ORDER BY held.org_id
$function$;

CREATE OR REPLACE FUNCTION public.lock_module_job_authority(stamp jsonb)
 RETURNS void
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
BEGIN
    PERFORM 1 FROM public.installations i
     WHERE i.id::text = stamp->>'codefly.installation_id'
       AND i.org_id::text = stamp->>'codefly.organization_id'
       AND i.registry_principal_id::text = stamp->>'codefly.principal_id'
       AND i.registry_binding_id = stamp->>'codefly.binding_id'
       AND i.revision::text = stamp->>'codefly.installation_revision'
       AND i.status = 'active' AND i.scope->>'capabilities' = 'true'
           AND (current_setting('role', true) = 'app_job_worker' OR (
                current_setting('role', true) = 'app_tenant' AND (
                    i.org_id::text = NULLIF(current_setting('codefly.current_tenant_id', true), '') OR
                    EXISTS (SELECT 1 FROM public.organization_members m
                      WHERE m.org_id = i.org_id
                        AND m.user_id::text = NULLIF(current_setting('codefly.current_user_id', true), '')))))
       FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'revoked_authority' USING ERRCODE = 'check_violation'; END IF;
END;
$function$;

CREATE OR REPLACE FUNCTION public.module_job_authority_current(stamp jsonb)
 RETURNS boolean
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT EXISTS (
        SELECT 1 FROM public.installations i
         WHERE i.id::text = stamp->>'codefly.installation_id'
           AND i.org_id::text = stamp->>'codefly.organization_id'
           AND i.registry_principal_id::text = stamp->>'codefly.principal_id'
           AND i.registry_binding_id = stamp->>'codefly.binding_id'
           AND i.revision::text = stamp->>'codefly.installation_revision'
           AND i.status = 'active' AND i.scope->>'capabilities' = 'true'
           AND (current_setting('role', true) = 'app_job_worker' OR (
                current_setting('role', true) = 'app_tenant' AND (
                    i.org_id::text = NULLIF(current_setting('codefly.current_tenant_id', true), '') OR
                    EXISTS (SELECT 1 FROM public.organization_members m
                      WHERE m.org_id = i.org_id
                        AND m.user_id::text = NULLIF(current_setting('codefly.current_user_id', true), '')))))
    );
$function$;

CREATE OR REPLACE FUNCTION public.organization_eligible_administrators(p_org_id uuid)
 RETURNS SETOF uuid
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
    SELECT member.user_id
    FROM public.organization_members AS member
    JOIN public.users AS u
      ON u.uuid = member.user_id
    WHERE member.org_id = p_org_id
      AND member.org_id::text = pg_catalog.current_setting('codefly.current_tenant_id', true)
      AND member.role IN ('owner', 'admin')
      AND u.status = 'active'
$function$;

CREATE OR REPLACE FUNCTION public.organization_member_primary_email(p_user_id uuid)
 RETURNS text
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
    SELECT u.primary_email
    FROM public.users AS u
    JOIN public.organization_members AS member
      ON member.user_id = u.uuid
    WHERE u.uuid = p_user_id
      AND member.org_id::text = pg_catalog.current_setting('codefly.current_tenant_id', true)
      AND u.status <> 'deleted'
    LIMIT 1
$function$;

CREATE OR REPLACE FUNCTION public.publish_domain_event(p_id uuid, p_type text, p_source text, p_subject text, p_event_time timestamp with time zone, p_specversion text, p_datacontenttype text, p_dataschema text, p_data bytea, p_tenant_id uuid, p_boundary_id text, p_partition_key text, p_correlation_id text, p_causation_id text, p_actor_principal_id text, p_owner_principal_id text, p_traceparent text, p_schema_version integer, p_request_fingerprint bytea)
 RETURNS TABLE(event_id uuid, stored_fingerprint bytea, inserted boolean)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    caller_role TEXT;
    inserted_id UUID;
BEGIN
    caller_role := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );

    IF caller_role = 'app_tenant' THEN
        -- IS DISTINCT FROM, not <>: a transaction that never set codefly.current_tenant_id
        -- (a user-scoped one, which runs as app_tenant and sets only
        -- codefly.current_user_id) leaves the setting NULL, and `p_tenant_id <> NULL`
        -- is NULL, not TRUE — so a plain comparison lets that transaction publish
        -- for any tenant it names. The guard has to fail closed on an absent scope.
        IF p_tenant_id IS NULL
           OR p_tenant_id IS DISTINCT FROM NULLIF(current_setting('codefly.current_tenant_id', true), '')::uuid THEN
            RAISE EXCEPTION 'event tenant does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role = 'app_control_plane' THEN
        -- The control plane publishes on behalf of a tenant. A platform-admin
        -- mutation writes its audit record with RLS bypassed, and that record's
        -- external domain event is what an outbound webhook subscription is
        -- fanned out from, so refusing a tenant here would silently stop
        -- delivering every privileged action to the endpoints subscribed to it.
        -- The role already writes any tenant's audit rows; publishing the
        -- matching event is the same authority, and the relay applies the tenant
        -- gate again when it resolves subscriptions.
        NULL;
    ELSIF caller_role <> 'app_job_worker' THEN
        RAISE EXCEPTION 'role % cannot publish domain events', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    -- Serialize concurrent publishes within one partition so that the identity
    -- seq assigned below is handed out in commit order, not merely in insert
    -- order. The lock is transaction-scoped: it is held until the producer's
    -- transaction commits or rolls back, which is exactly the window in which a
    -- competing producer could otherwise take a higher seq and commit first.
    -- Ordering is a per-partition guarantee, so the lock is per-partition; a
    -- platform event with no partition key is unordered by construction
    -- (eventOrdering returns no ordering key for it) and takes no lock, so it
    -- never serializes against anything.
    IF COALESCE(p_partition_key, '') <> '' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('events:' || p_partition_key, 0));
    END IF;

    INSERT INTO public.domain_events (
        id, type, source, subject, event_time, specversion, datacontenttype,
        dataschema, data, tenant_id, boundary_id, partition_key, correlation_id,
        causation_id, actor_principal_id, owner_principal_id, traceparent,
        schema_version, request_fingerprint
    ) VALUES (
        p_id, p_type, p_source, COALESCE(p_subject, ''), p_event_time,
        COALESCE(p_specversion, ''), COALESCE(p_datacontenttype, ''),
        COALESCE(p_dataschema, ''), COALESCE(p_data, ''::bytea), p_tenant_id,
        COALESCE(p_boundary_id, ''), COALESCE(p_partition_key, ''),
        COALESCE(p_correlation_id, ''), COALESCE(p_causation_id, ''),
        COALESCE(p_actor_principal_id, ''), COALESCE(p_owner_principal_id, ''),
        COALESCE(p_traceparent, ''), COALESCE(p_schema_version, 1),
        p_request_fingerprint
    )
    ON CONFLICT (id) DO NOTHING
    RETURNING id INTO inserted_id;

    IF inserted_id IS NOT NULL THEN
        RETURN QUERY SELECT inserted_id, p_request_fingerprint, TRUE;
        RETURN;
    END IF;

    RETURN QUERY
    SELECT existing.id, existing.request_fingerprint, FALSE
    FROM public.domain_events AS existing
    WHERE existing.id = p_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'domain event idempotency conflict could not be resolved'
            USING ERRCODE = 'serialization_failure';
    END IF;
END
$function$;

CREATE OR REPLACE FUNCTION public.record_membership_integrity_findings()
 RETURNS integer
 LANGUAGE plpgsql
 SET search_path TO 'pg_catalog', 'public'
AS $function$
DECLARE
    outstanding integer;
BEGIN
    -- An inventory has to see every organization, and organizations,
    -- organization_members and users (administrator eligibility depends on
    -- identity status) are all FORCE ROW LEVEL SECURITY — which subjects the
    -- table owner to their policies as well. Those policies scope to
    -- codefly.current_tenant_id and carry no bypass clause (a session setting must
    -- never grant RLS authority; the role-hardening suite asserts no live
    -- policy reads one). A caller that does not span organizations therefore
    -- reads zero rows, records nothing, and returns 0 — a result identical to a
    -- healthy platform and one nobody would think to question. Refuse instead.
    IF current_user <> 'app_control_plane' THEN
        RAISE EXCEPTION
            'record_membership_integrity_findings() must run as a role that spans every organization; % does not, and would record an empty backlog',
            current_user
            USING HINT = 'assume the control plane first: SET ROLE app_control_plane';
    END IF;

    WITH org_administration AS (
        -- One pass over the membership graph, so every finding below is decided
        -- from the same per-organization facts rather than from repeated
        -- correlated subqueries that could drift apart.
        --
        -- "Eligible" is migration 133's definition, deliberately reproduced
        -- rather than approximated: role IN ('owner','admin') AND the identity
        -- is active. findIdentity admits only active identities and DeleteUser
        -- is a soft delete that leaves the membership row standing, so counting
        -- every administrative row regardless of identity status reports an
        -- organization healthy when nobody can actually sign in and administer
        -- it. That organization is precisely the backlog this inventory exists
        -- to size. Migration 133's organization_eligible_administrators() is
        -- not reused here: it is SECURITY DEFINER scoped to codefly.current_tenant_id,
        -- so it answers for one tenant, and this is a cross-tenant inventory.
        SELECT
            o.id       AS org_id,
            o.owner_id AS owner_id,
            (
                SELECT om.role FROM organization_members om
                WHERE om.org_id = o.id AND om.user_id = o.owner_id
            ) AS owner_membership_role,
            (
                SELECT u.status::text FROM users u WHERE u.uuid = o.owner_id
            ) AS owner_status,
            count(*) FILTER (
                WHERE member.role IN ('owner', 'admin')
            ) AS administrative_members,
            count(*) FILTER (
                WHERE member.role IN ('owner', 'admin') AND holder.status = 'active'
            ) AS eligible_administrators,
            count(*) FILTER (
                WHERE member.role IN ('owner', 'admin') AND holder.status = 'active'
                  AND member.user_id = o.owner_id
            ) AS owner_is_eligible
        FROM organizations o
        LEFT JOIN organization_members member ON member.org_id = o.id
        LEFT JOIN users holder ON holder.uuid = member.user_id
        GROUP BY o.id, o.owner_id
    ),
    current_findings AS (
        -- The findings are mutually exclusive by construction: one CASE over
        -- one row per organization can only ever yield one of them. The
        -- previous shape kept three disjoint WHERE clauses in step by hand,
        -- which is the kind of agreement that silently stops holding. An
        -- organization is therefore never counted twice, so the row count this
        -- function returns is an organization count -- the number an operator
        -- sizes the repair from.
        SELECT
            org_id,
            CASE
                -- No administrative membership exists at all. Repairing this
                -- means choosing a user and granting them authority.
                WHEN administrative_members = 0
                    THEN 'organization_without_administrator'
                -- Administrative memberships exist, but every holder is
                -- deleted, suspended or otherwise not active. Materially
                -- cheaper to repair -- reactivating one identity may be
                -- enough -- and a different operator decision, which is why it
                -- is reported apart from the case above rather than folded in.
                WHEN eligible_administrators = 0
                    THEN 'organization_without_an_eligible_administrator'
                -- Somebody eligible can administer this organization, but it is
                -- not the owner of record: either the owner holds no
                -- administrative membership, or holds one whose identity is not
                -- active. owner_status and membership_role tell those apart.
                ELSE 'owner_of_record_is_not_an_administrator'
            END AS finding,
            jsonb_build_object(
                'owner_id', owner_id,
                'membership_role', owner_membership_role,
                'owner_status', owner_status,
                'administrative_members', administrative_members,
                'eligible_administrators', eligible_administrators
            ) AS detail
        FROM org_administration
        WHERE administrative_members  = 0
           OR eligible_administrators = 0
           OR owner_is_eligible       = 0
    ),
    resolved AS (
        DELETE FROM membership_integrity_findings f
        WHERE NOT EXISTS (
            SELECT 1 FROM current_findings c
            WHERE c.org_id = f.org_id AND c.finding = f.finding
        )
    )
    INSERT INTO membership_integrity_findings (org_id, finding, detail)
    SELECT org_id, finding, detail FROM current_findings
    ON CONFLICT (org_id, finding) DO UPDATE SET detail = EXCLUDED.detail;

    SELECT count(*) INTO outstanding FROM membership_integrity_findings;
    RETURN outstanding;
END
$function$;

CREATE OR REPLACE FUNCTION public.sync_webhook_event_subscriptions(p_org_id uuid, p_webhook_subscription_id uuid, p_type_patterns text[])
 RETURNS void
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE
    caller_role TEXT;
    owner_org   UUID;
BEGIN
    caller_role := COALESCE(
        NULLIF(current_setting('role', true), 'none'),
        session_user::text
    );

    IF caller_role = 'app_tenant' THEN
        IF p_org_id IS NULL
           OR p_org_id IS DISTINCT FROM NULLIF(current_setting('codefly.current_tenant_id', true), '')::uuid THEN
            RAISE EXCEPTION 'webhook subscription org does not match the signed request scope'
                USING ERRCODE = 'insufficient_privilege';
        END IF;
    ELSIF caller_role NOT IN ('app_control_plane', 'app_job_worker') THEN
        RAISE EXCEPTION 'role % cannot manage webhook event subscriptions', caller_role
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    -- The endpoint must belong to the org the subscriptions are being bound to,
    -- so a caller holding one org's scope cannot attach a subscription to another
    -- org's endpoint by naming its id.
    SELECT org_id INTO owner_org
    FROM public.webhook_subscriptions
    WHERE id = p_webhook_subscription_id;
    IF owner_org IS NULL OR owner_org <> p_org_id THEN
        RAISE EXCEPTION 'webhook subscription does not belong to this organization'
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    DELETE FROM public.event_subscriptions
    WHERE webhook_subscription_id = p_webhook_subscription_id
      AND type_pattern <> ALL(COALESCE(p_type_patterns, ARRAY[]::text[]));

    INSERT INTO public.event_subscriptions
        (subscriber_principal_id, type_pattern, queue, delivery, org_id, webhook_subscription_id)
    SELECT NULL::uuid, pattern, 'webhooks', 'webhook', p_org_id, p_webhook_subscription_id
    FROM unnest(COALESCE(p_type_patterns, ARRAY[]::text[])) AS pattern
    ON CONFLICT DO NOTHING;
END
$function$;

-- Every partition of public.audit_events carries its own copy of the parent's
-- policies: PostgreSQL applies a partitioned table's policies only to a query
-- that names the parent, so a partition read by name is judged by its own.
-- Their names are not known to this file -- migration 34's
-- audit_events_secure_partition drops a partition's policies and clones the
-- parent's, which is the ledger's own mechanism for exactly this, and the one
-- the runtime partition creator already calls. Nothing else here knows how to
-- rewrite a policy whose name depends on the month.
DO $$
DECLARE
    part regclass;
BEGIN
    FOR part IN
        SELECT i.inhrelid::regclass
          FROM pg_catalog.pg_inherits i
         WHERE i.inhparent = 'public.audit_events'::regclass
         ORDER BY i.inhrelid::regclass::text
    LOOP
        PERFORM public.audit_events_secure_partition(part);
    END LOOP;
END;
$$;
