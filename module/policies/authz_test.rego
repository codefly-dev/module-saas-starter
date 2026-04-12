package authz_test

import data.authz
import rego.v1

# ── Public endpoints ─────────────────────────────────────────────

test_public_register_always_allowed if {
	authz.allow with input as {
		"method": "/user.v1.UserService/RegisterUser",
		"user_id": "",
		"org_role": "",
		"platform_role": "",
	}
}

test_public_authenticate_always_allowed if {
	authz.allow with input as {
		"method": "/user.v1.AuthService/Authenticate",
		"user_id": "",
		"org_role": "",
		"platform_role": "",
	}
}

test_public_jwks_always_allowed if {
	authz.allow with input as {
		"method": "/user.v1.AuthService/GetJWKS",
		"user_id": "",
		"org_role": "",
		"platform_role": "",
	}
}

# ── Authenticated endpoints ──────────────────────────────────────

test_authenticated_get_self_with_user if {
	authz.allow with input as {
		"method": "/user.v1.UserService/GetSelf",
		"user_id": "user-123",
		"org_role": "",
		"platform_role": "",
	}
}

test_authenticated_get_self_denied_without_user if {
	not authz.allow with input as {
		"method": "/user.v1.UserService/GetSelf",
		"user_id": "",
		"org_role": "",
		"platform_role": "",
	}
}

# ── Platform-only endpoints ──────────────────────────────────────

test_platform_super_admin_can_suspend if {
	authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/SuspendUser",
		"user_id": "admin-1",
		"org_role": "",
		"platform_role": "super_admin",
	}
}

test_platform_support_cannot_suspend if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/SuspendUser",
		"user_id": "support-1",
		"org_role": "",
		"platform_role": "support",
	}
}

test_platform_billing_cannot_suspend if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/SuspendUser",
		"user_id": "billing-1",
		"org_role": "",
		"platform_role": "billing",
	}
}

test_platform_support_can_search_users if {
	authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/SearchUsers",
		"user_id": "support-1",
		"org_role": "",
		"platform_role": "support",
	}
}

test_platform_support_can_impersonate if {
	authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/ImpersonateUser",
		"user_id": "support-1",
		"org_role": "",
		"platform_role": "support",
	}
}

test_platform_billing_can_view_entitlements if {
	authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/GetOrgEntitlements",
		"user_id": "billing-1",
		"org_role": "",
		"platform_role": "billing",
	}
}

test_platform_billing_can_override_entitlements if {
	authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/OverrideEntitlement",
		"user_id": "billing-1",
		"org_role": "",
		"platform_role": "billing",
	}
}

test_platform_support_cannot_override_entitlements if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/OverrideEntitlement",
		"user_id": "support-1",
		"org_role": "",
		"platform_role": "support",
	}
}

test_platform_only_super_admin_can_grant_role if {
	authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/GrantPlatformRole",
		"user_id": "admin-1",
		"org_role": "",
		"platform_role": "super_admin",
	}
}

test_platform_support_cannot_grant_role if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/GrantPlatformRole",
		"user_id": "support-1",
		"org_role": "",
		"platform_role": "support",
	}
}

test_platform_org_admin_cannot_call_platform_endpoints if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/SuspendUser",
		"user_id": "user-1",
		"org_role": "admin",
		"platform_role": "",
	}
}

# ── Org-scoped endpoints ─────────────────────────────────────────

test_org_admin_can_invite if {
	authz.allow with input as {
		"method": "/user.v1.InvitationService/CreateInvitation",
		"user_id": "user-1",
		"org_role": "admin",
		"platform_role": "",
	}
}

test_org_member_cannot_invite if {
	not authz.allow with input as {
		"method": "/user.v1.InvitationService/CreateInvitation",
		"user_id": "user-2",
		"org_role": "member",
		"platform_role": "",
	}
}

test_org_member_can_list_teams if {
	authz.allow with input as {
		"method": "/user.v1.TeamService/ListTeams",
		"user_id": "user-2",
		"org_role": "member",
		"platform_role": "",
	}
}

test_org_owner_can_do_admin_ops if {
	authz.allow with input as {
		"method": "/user.v1.OrganizationService/AddMember",
		"user_id": "owner-1",
		"org_role": "owner",
		"platform_role": "",
	}
}

test_org_admin_can_create_api_key if {
	authz.allow with input as {
		"method": "/user.v1.APIKeyService/CreateAPIKey",
		"user_id": "admin-1",
		"org_role": "admin",
		"platform_role": "",
	}
}

test_org_member_can_list_api_keys if {
	authz.allow with input as {
		"method": "/user.v1.APIKeyService/ListAPIKeys",
		"user_id": "member-1",
		"org_role": "member",
		"platform_role": "",
	}
}

test_org_member_cannot_revoke_api_key if {
	not authz.allow with input as {
		"method": "/user.v1.APIKeyService/RevokeAPIKey",
		"user_id": "member-1",
		"org_role": "member",
		"platform_role": "",
	}
}

# ── Super admin god mode ─────────────────────────────────────────

test_super_admin_can_access_org_scoped_endpoints if {
	authz.allow with input as {
		"method": "/user.v1.InvitationService/CreateInvitation",
		"user_id": "admin-1",
		"org_role": "",
		"platform_role": "super_admin",
	}
}

test_super_admin_bypasses_org_role_for_team_ops if {
	authz.allow with input as {
		"method": "/user.v1.TeamService/CreateTeam",
		"user_id": "admin-1",
		"org_role": "",
		"platform_role": "super_admin",
	}
}

# ── Default deny ─────────────────────────────────────────────────

test_unknown_method_denied if {
	not authz.allow with input as {
		"method": "/user.v1.UnknownService/DoSomething",
		"user_id": "user-1",
		"org_role": "admin",
		"platform_role": "super_admin",
	}
}

test_no_auth_denied_on_org_endpoint if {
	not authz.allow with input as {
		"method": "/user.v1.OrganizationService/AddMember",
		"user_id": "",
		"org_role": "",
		"platform_role": "",
	}
}

# ── Impersonation restrictions ───────────────────────────────────

test_impersonated_can_read_users if {
	authz.allow with input as {
		"method": "/user.v1.UserService/GetSelf",
		"user_id": "target-1",
		"org_role": "",
		"platform_role": "",
		"is_impersonated": true,
	}
}

test_impersonated_can_list_orgs if {
	authz.allow with input as {
		"method": "/user.v1.OrganizationService/ListOrganizations",
		"user_id": "target-1",
		"org_role": "",
		"platform_role": "",
		"is_impersonated": true,
	}
}

test_impersonated_blocked_from_update_user if {
	not authz.allow with input as {
		"method": "/user.v1.UserService/UpdateUser",
		"user_id": "target-1",
		"org_role": "admin",
		"platform_role": "",
		"is_impersonated": true,
	}
}

test_impersonated_blocked_from_create_api_key if {
	not authz.allow with input as {
		"method": "/user.v1.APIKeyService/CreateAPIKey",
		"user_id": "target-1",
		"org_role": "admin",
		"platform_role": "",
		"is_impersonated": true,
	}
}

test_impersonated_blocked_from_impersonating_again if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/ImpersonateUser",
		"user_id": "target-1",
		"org_role": "",
		"platform_role": "super_admin",
		"is_impersonated": true,
	}
}

test_impersonated_blocked_from_suspend if {
	not authz.allow with input as {
		"method": "/user.v1.PlatformAdminService/SuspendUser",
		"user_id": "target-1",
		"org_role": "",
		"platform_role": "super_admin",
		"is_impersonated": true,
	}
}

test_impersonated_blocked_from_assign_role if {
	not authz.allow with input as {
		"method": "/user.v1.PermissionService/AssignRole",
		"user_id": "target-1",
		"org_role": "admin",
		"platform_role": "",
		"is_impersonated": true,
	}
}

test_impersonated_can_list_teams if {
	authz.allow with input as {
		"method": "/user.v1.TeamService/ListTeams",
		"user_id": "target-1",
		"org_role": "member",
		"platform_role": "",
		"is_impersonated": true,
	}
}

test_non_impersonated_not_blocked if {
	authz.allow with input as {
		"method": "/user.v1.UserService/UpdateUser",
		"user_id": "user-1",
		"org_role": "admin",
		"platform_role": "",
		"is_impersonated": false,
	}
}
