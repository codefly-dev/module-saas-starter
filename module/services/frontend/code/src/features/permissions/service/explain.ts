"use client";

import { useQuery } from "@tanstack/react-query";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import { usePermissionService } from "@/lib/hooks/use-api-client";
import type { Permission } from "../model/effective";

// The decision point's own answer, asked one question at a time.
//
// This is the authoritative half of the grant check: the handler funnels into
// the same store query the internal CheckPermission returns, so what comes
// back is the verdict the service would give a caller, not a reading of the
// rows beside it. The client-side resolution stays for provenance — which role,
// direct or via which team — because the RPC does not return a path.
//
// Not cached: an administrator uses this to verify a grant before relying on
// it, and a stale yes is the one answer this control must never give.
export function useExplainPermission(
	orgId: string,
	subjectId: string,
	wanted: Permission | undefined,
	scope: string,
) {
	const service = usePermissionService();
	return useQuery({
		queryKey: [
			"explain-permission",
			orgId,
			subjectId,
			wanted ? `${wanted.resource}:${wanted.action}` : "",
			scope,
		],
		enabled: !!orgId && !!subjectId && !!wanted,
		gcTime: 0,
		staleTime: 0,
		queryFn: () =>
			service.explainPermission({
				orgId,
				subjectId,
				// The org member picker names people, and a person is a principal.
				// The unspecified value is refused by the contract rather than
				// guessed, so it is never sent.
				subjectKind: SubjectKind.PRINCIPAL,
				resource: wanted?.resource ?? "",
				action: wanted?.action ?? "",
				scope,
			}),
	});
}
