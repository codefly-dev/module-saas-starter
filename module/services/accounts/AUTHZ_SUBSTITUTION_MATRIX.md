# Authorization substitution matrix

This matrix is the checked-in evidence for actor, tenant, and resource-ID
substitution resistance. Transport admission is covered by
`rpc_policy_interceptor_test.go`; the tests below exercise business ownership
and PostgreSQL RLS with two independent principals and exact foreign IDs.

| Surface | Allowed control | Substitution attempted | Enforcement layers | Regression test |
|---|---|---|---|---|
| Organizations | Tenant reads its own organization | Org A supplies Org B UUID | handler tenant scope + self-referential organization RLS | `TestRLS_Organizations_SelfReferential` |
| Teams | Organization member reads its tenant's teams | Org A queries Org B team UUID | membership check + teams RLS | `TestRLS_Teams_CrossTenantBlocked` |
| Team membership | Team member reads members of its team | Org A queries members using Org B team UUID | parent-team authorization + join-based RLS | `TestRLS_TeamMembers_PolicyJoinsToParent` |
| Roles | Authorized tenant administers its role assignments | Org A supplies Org B role/assignment IDs | org-admin handler gate + role-assignment RLS | `TestRLS_RoleAssignments_CrossTenantBlocked`, `TestE2E_RoleAssignmentFlow` |
| API keys | Org admin creates/lists/revokes keys in its organization | Org A queries Org B keys | org-admin gate + organization-bound key queries + RLS | `TestRLS_APIKeys_CrossTenantBlocked` |
| Webhook subscriptions | Org admin manages its subscriptions | Org A supplies Org B subscription UUID | org-admin gate + subscription RLS | `TestRLS_WebhookSubscriptions_CrossTenantBlocked` |
| Webhook deliveries | Org admin inspects/replays its deliveries | Org A supplies a delivery whose parent belongs to Org B | parent subscription lookup + join-based delivery RLS | `TestRLS_WebhookDeliveries_PolicyJoinsToParent` |
| Notifications | User reads/mutates their notifications | User A supplies User B notification UUID | explicit owner comparison + user-scoped RLS | `TestRLS_Notifications_CrossUserBlocked` |
| GDPR requests | Subject reads export/deletion status of their request | User A supplies User B request UUID, or swaps export/deletion RPC | subject/type comparison + user-scoped RLS | `TestGDPRStatusIsBoundToSubjectAndRequestType` |
| MFA devices | User lists/removes their factors | User A queries User B factor rows | authenticated subject binding + user-scoped RLS | `TestRLS_MFADevices_CrossUserBlocked` |
| MFA backup codes | User generates/uses their recovery codes | User A queries User B codes | authenticated subject binding + user-scoped RLS | `TestRLS_MFABackupCodes_CrossUserBlocked` |
| Raw tenant store access | Scoped transaction accesses its tenant | Missing tenant/user context or a foreign explicit filter | `FORCE ROW LEVEL SECURITY`, scoped transaction GUCs | all tests above include unwrapped fail-closed probes; `TestWithUserTx_RejectsEmptyUserID` |

Required actor rows for handler-level matrices are anonymous, unrelated tenant
member, same-tenant member, admin/owner, resource owner, platform admin, API
key, and internal workload. RPC admission coverage must stay protocol-parity
tested for Connect and gRPC; resource tests must always use real foreign UUIDs,
not nonexistent IDs.
