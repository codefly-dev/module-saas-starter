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
// caller knows two attempts are the same revocation. None of these four RPCs
// carries one — there is no such field on the request messages and no ambient
// request key to derive one from — so this host genuinely cannot recognise a
// retry. The two ways to cope are not symmetric:
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
