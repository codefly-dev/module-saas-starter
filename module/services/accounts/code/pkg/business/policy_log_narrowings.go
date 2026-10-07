package business

import "fmt"

// The narrowings that go through the policy log, and how each one names itself
// to it.
//
// Every function here builds one PolicyLogEntry. None of them performs the
// narrowing: the call sites wrap their own domain logic in
// WithPolicyLoggedNarrowing with the entry built here, so there is exactly one
// description of each operation and it is the one the log receives.
//
// WHY THE OPERATION ID IS FRESH ON EVERY ATTEMPT, and why that is the safe
// direction. The protocol wants the CALLER'S idempotency key, because only the
// caller knows two attempts are the same revocation. No RPC behind these
// entries carries one — there is no such field on the request messages and no
// ambient request key to derive one from — so this host genuinely cannot
// recognise a retry. The two ways to cope are not symmetric:
//
//   - derive the id from the operation's inputs. A retry then collides and
//     appends once, which is what the protocol asks for. But a narrowing that
//     is reachable AGAIN after an authorised regrant — revoke, re-grant, revoke
//     — derives the SAME id the second time, the log returns the first
//     receipt, the local commit row is already committed, and the second
//     revocation applies with nothing having witnessed it. That is an
//     unwitnessed narrowing, the one outcome the protocol forbids outright.
//   - a fresh id per attempt. A retry appends twice, so the log over-records.
//     Harmless: an entry carries the authority as it stands AFTER the operation
//     rather than a diff, so two entries for one revocation replay to the same
//     answer, and the warehouse's replay view collapses per operation id.
//
// Over-recording is noise; under-recording is a restore that silently undoes a
// revocation. So the id is fresh, and the real fix — an idempotency key on the
// request messages, carried from the caller to here — is a contract change
// these entries will take the moment it exists.
func newPolicyLogOperationID(operation string) string {
	return operation + "/" + NewIDString()
}

// uninstallPolicyLogEntry describes revoking an installation: the agent
// principal loses its standing grant and the installation becomes revoked.
func uninstallPolicyLogEntry(actorID, orgID, installationID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("uninstall-solution"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogInstallation,
		SubjectID:   installationID,
		Policy: map[string]any{
			"org_id": orgID,
			"status": "revoked",
		},
		Actor: actorID,
	}
}

// closedSolutionTargetPolicyLogEntry describes a withdrawn presence: the target
// is closed and every active installation naming it is revoked with it.
//
// The subject is the TARGET rather than each installation it revokes. The target
// is the identity whose consent period ended, and it is the one a replay can ask
// about: an installation revoked by a close is revoked because the target
// closed, so an entry per installation would record the same fact several times
// and leave a reader to work out which close they belonged to.
//
// The operation id is passed IN rather than generated here, because the apply
// transaction has to recognise the close it was appended for: the id is the
// witness reconcileSolutionTarget requires, and a second id generated here
// would leave the two halves naming different operations.
func closedSolutionTargetPolicyLogEntry(
	operationID, targetID, bindingID, solutionID string, generation uint64,
) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: operationID,
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogInstallation,
		SubjectID:   targetID,
		Policy: map[string]any{
			"binding_id":        bindingID,
			"solution_id":       solutionID,
			"closed_generation": generation,
			"status":            "closed",
			"installations":     "revoked",
		},
		// The generation the withdrawal was decided against, so a replay can
		// tell a close made under an older delivery from one under the current.
		EnvelopeRevision: generation,
		Actor:            "solution:" + solutionID,
	}
}

// revokeScopePolicyLogEntry describes removing one hierarchical scope grant.
//
// The subject id spells the grant out rather than naming a row id, because the
// revocation itself is addressed that way: scope_grants is deleted by
// (org, subject, kind, path, role) and has no id the caller holds. A replay
// therefore reads the same key the narrowing used.
func revokeScopePolicyLogEntry(actorID, orgID, subjectID, subjectKind, scopePath, roleID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-scope"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogScopeGrant,
		SubjectID: fmt.Sprintf("%s/%s/%s/%s/%s",
			orgID, subjectKind, subjectID, scopePath, roleID),
		Policy: map[string]any{
			"org_id":       orgID,
			"subject_id":   subjectID,
			"subject_kind": subjectKind,
			"scope_path":   scopePath,
			"role_id":      roleID,
			"status":       "revoked",
		},
		Actor: actorID,
	}
}

// removeTeamMemberPolicyLogEntry describes removing a membership. A team is a
// subject permissions are granted to, so losing a membership narrows what the
// removed user may do through it.
func removeTeamMemberPolicyLogEntry(actorID, orgID, teamID, userID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("remove-team-member"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogTeamMembership,
		SubjectID:   teamID + "/" + userID,
		Policy: map[string]any{
			"org_id":  orgID,
			"team_id": teamID,
			"user_id": userID,
			"status":  "removed",
		},
		Actor: actorID,
	}
}

// ---------------------------------------------------------------------------
// How a narrowing names its subject
//
// PolicyLogSubjectKind is a closed set — an operation whose subject this host
// cannot name is one a reconciliation cannot act on — so every entry below
// reports itself as one of the kinds that set admits. Which one is a uniform
// rule rather than per-call-site taste, because a reader comparing two entries
// has to be able to assume the kinds mean the same thing in both:
//
//   - PolicyLogScopeGrant — a GRANT ROW addressed by its tuple: a hierarchical
//     scope grant, a role assignment, a per-record share. Each one is "this
//     role, to this subject, over this thing", and each is revoked by naming
//     the tuple rather than a row id the caller holds, so the subject id
//     spells the tuple out and a replay reads the same key the revocation used.
//   - PolicyLogBinding — the STANDING AUTHORITY a subject holds in an estate,
//     which is what grants hang from rather than a grant itself: an
//     organization membership, a platform role, a source delegation.
//   - PolicyLogPrincipal — the ACTING IDENTITY itself. Nothing about what some
//     subject may do is narrowed; an actor stops being able to act at all.
//   - PolicyLogInstallation, PolicyLogTeamMembership — as the entries above
//     this block already use them.
//
// LABELLED STOPGAP, and the fix belongs in policy_log.go. An organization is a
// subject this host narrows — archiving one revokes every authority inside it
// — and the closed set has no kind that names it. deletedOrganizationPolicyLog
// Entry below therefore reports PolicyLogBinding, which is the nearest true
// statement (the archive's whole effect is that every standing binding into the
// organization is gone) but not the exact one: a reconciliation reading that
// entry looks for a binding whose id is an organization's. The fix is a
// PolicyLogOrganization constant in the closed set, and this entry takes it the
// moment it exists.

// deletedOrganizationPolicyLogEntry describes archiving an organization: its
// API keys, pending invitations, installations, source delegations and every
// membership are revoked together, and with no membership left every
// request-path authorization refuses the organization.
//
// ONE ENTRY, NOT ONE PER REVOCATION, and the reason is the one
// closedSolutionTargetPolicyLogEntry gives: everything this operation revokes
// is revoked BECAUSE the organization was archived, so an entry per
// installation — or per membership, or per key — would record the same fact
// several times and leave a reader to work out which archive they belonged to.
// The organization is the identity whose authority ended, and it is the one a
// replay can ask about.
//
// It also could not be done the other way round. The per-installation entries
// would have to be appended from inside the archive's own transaction, and
// WithPolicyLoggedNarrowing cannot nest: each call opens its own
// WithControlPlane transaction, and tenant_tx.go's no-nesting rule is that a
// second transaction checks out a second pool connection while the first still
// holds one. So the archive revokes its installations through
// uninstallSolutionTx — the same function an operator's uninstall applies —
// under this one witnessed operation, rather than through UninstallSolution,
// which would try to witness each of them separately.
//
// Policy names what the archive revoked rather than listing the rows it
// touched: the ids would have to be read before the append, outside the
// transaction that does the work, and a list read then is a list that may have
// changed by the time the archive runs. What is true after the operation is
// that none of them is live, which is what an entry is for.
func deletedOrganizationPolicyLogEntry(actorID, orgID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("delete-organization"),
		Decision:    PolicyLogNarrowed,
		// The organization itself. This reported PolicyLogBinding while the
		// closed set had no kind that named an organization — a labelled
		// stopgap, because an archive is not a binding and a reconciliation
		// reading it would look for a binding whose id is an organization's.
		// PolicyLogOrganization now exists, so the entry says what it means.
		SubjectKind: PolicyLogOrganization,
		SubjectID:   orgID,
		Policy: map[string]any{
			"org_id":              orgID,
			"status":              "archived",
			"memberships":         "removed",
			"api_keys":            "revoked",
			"pending_invitations": "revoked",
			"installations":       "revoked",
			"source_delegations":  "revoked",
		},
		Actor: actorID,
	}
}

// revokeRolePolicyLogEntry describes removing one role assignment: the subject
// stops holding that role, in that organization, at that scope.
//
// The subject id spells the assignment out rather than naming a row id, for the
// reason revokeScopePolicyLogEntry does: role_assignments is revoked by
// (subject, role, org, scope) and the caller holds no id for the row. An empty
// org is a PLATFORM-level assignment and an empty scope is the organization
// whole, and both are spelled as the empty segment they are — the store matches
// them with IS NOT DISTINCT FROM, so "" and "no org" are the same key there and
// must stay the same key here.
func revokeRolePolicyLogEntry(actorID, orgID, subjectID, roleID, scope string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-role"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogScopeGrant,
		SubjectID:   fmt.Sprintf("%s/%s/%s/%s", orgID, subjectID, roleID, scope),
		Policy: map[string]any{
			"org_id":     orgID,
			"subject_id": subjectID,
			"role_id":    roleID,
			"scope":      scope,
			"status":     "revoked",
		},
		Actor: actorID,
	}
}

// revokeSharePolicyLogEntry describes removing one per-record share: the
// subject stops holding that role on that one record.
//
// The subject id spells the share out for the same reason the two entries above
// do — record_shares is deleted by (org, resource type, resource, subject,
// subject kind, role) and has no id the caller holds.
func revokeSharePolicyLogEntry(
	actorID, orgID, resourceType, resourceID, subjectID, subjectKind, roleID string,
) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-share"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogScopeGrant,
		SubjectID: fmt.Sprintf("%s/%s/%s/%s/%s/%s",
			orgID, resourceType, resourceID, subjectKind, subjectID, roleID),
		Policy: map[string]any{
			"org_id":        orgID,
			"resource_type": resourceType,
			"resource_id":   resourceID,
			"subject_id":    subjectID,
			"subject_kind":  subjectKind,
			"role_id":       roleID,
			"status":        "revoked",
		},
		Actor: actorID,
	}
}

// revokePrincipalPolicyLogEntry describes revoking a principal: the identity
// itself stops being able to act, so nothing it was granted matters any more.
//
// The actor is passed in rather than taken from the request because
// RevokePrincipal has none — it is reached by operators and by internal
// incident paths, and it records its own audit event as the system. An entry
// with no actor is refused by Validate, so the caller states who it is acting
// as and the log carries that rather than an empty attribution.
//
// The organization is in Policy and not in the subject id: a human principal is
// org-less by schema (principals_org_scope), so the organization is a fact about
// this principal rather than part of its name.
func revokePrincipalPolicyLogEntry(actorID, principalID, orgID, reason string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-principal"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogPrincipal,
		SubjectID:   principalID,
		Policy: map[string]any{
			"org_id":       orgID,
			"principal_id": principalID,
			"status":       "revoked",
			"reason":       reason,
		},
		Actor: actorID,
	}
}

// revokeSourceDelegationPolicyLogEntry describes revoking one source
// delegation: the module binding stops holding the owner's authority over that
// source, and every later mint from it is refused.
func revokeSourceDelegationPolicyLogEntry(actorID, orgID, delegationID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-source-delegation"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogBinding,
		SubjectID:   delegationID,
		Policy: map[string]any{
			"org_id":        orgID,
			"delegation_id": delegationID,
			"status":        "revoked",
			"reason":        SourceDelegationRevokedByAdmin,
		},
		Actor: actorID,
	}
}

// removeOrgMemberPolicyLogEntry describes removing an organization membership:
// the member loses the standing every grant inside that organization hangs
// from, and the team memberships that outlive it go in the same transaction.
//
// The subject is the membership — the (organization, user) pair the removal
// addresses — rather than the user, because the user may still be a member of
// other organizations and a replay asking "what is this subject's authority"
// must not read the entry as "this person holds nothing anywhere".
func removeOrgMemberPolicyLogEntry(actorID, orgID, userID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("remove-org-member"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogBinding,
		SubjectID:   orgID + "/" + userID,
		Policy: map[string]any{
			"org_id":             orgID,
			"user_id":            userID,
			"status":             "removed",
			"team_memberships":   "removed",
			"source_delegations": "revoked",
		},
		Actor: actorID,
	}
}

// revokeAPIKeyPolicyLogEntry describes revoking an API key: the credential
// stops authenticating, so the identity it presented stops being able to act
// through it.
//
// The subject kind is the principal rather than a grant, because a key is not a
// role granted to somebody — it is an actor. Revoking it takes nothing away
// from the user who created it; it removes one of the ways that authority could
// be presented.
func revokeAPIKeyPolicyLogEntry(actorID, orgID, keyID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-api-key"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogPrincipal,
		SubjectID:   keyID,
		Policy: map[string]any{
			"org_id":     orgID,
			"api_key_id": keyID,
			"status":     "revoked",
		},
		Actor: actorID,
	}
}

// revokePlatformRolePolicyLogEntry describes removing a user's platform role:
// the widest authority this host grants, held across every organization rather
// than inside one.
//
// The subject is the user and the subject id carries no role, because the
// revocation does not name one either — platform_admins holds at most one row
// per user and the delete names the user alone. Policy says the authority that
// remains, which is none.
func revokePlatformRolePolicyLogEntry(actorID, userID string) *PolicyLogEntry {
	return &PolicyLogEntry{
		OperationID: newPolicyLogOperationID("revoke-platform-role"),
		Decision:    PolicyLogNarrowed,
		SubjectKind: PolicyLogBinding,
		SubjectID:   userID,
		Policy: map[string]any{
			"user_id":       userID,
			"scope":         "platform",
			"platform_role": "none",
			"status":        "revoked",
		},
		Actor: actorID,
	}
}
