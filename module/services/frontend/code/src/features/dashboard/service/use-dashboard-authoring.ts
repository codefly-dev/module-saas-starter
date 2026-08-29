"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { auditEventTypesQuery } from "@/features/audit/service/queries";
import { useAuth } from "@/lib/auth";
import { useAuditService } from "@/lib/hooks/use-api-client";
import type { DashboardDef } from "../model/schema";
import { createDashboardAuthoring, type DashboardAuthoring } from "./authoring";
import { type DashboardDraft, useDashboardDraft } from "./use-dashboard-draft";

// useDashboardAuthoring binds the driver-facing contract to the live audit
// client, the viewer's organization, and the canonical draft (useDashboardDraft
// from #317). setDashboard commits through the draft's setSpec, so a validated
// spec both persists and re-renders the mounted canvas; the returned draft is
// what a host renders and what surfaces a rejected persist.
export function useDashboardAuthoring(
	storageKey: string,
	initial: DashboardDef,
): { authoring: DashboardAuthoring; draft: DashboardDraft } {
	const audit = useAuditService();
	const queryClient = useQueryClient();
	const { organizationId } = useAuth();
	const orgId = organizationId ?? "";
	const draft = useDashboardDraft(storageKey, initial);
	const commit = draft.setSpec;
	// Back the injected vocabulary read with react-query's shared cache, so the
	// authoring surface and useAuditEventTypes read one cached registry entry.
	const readEventTypes = useCallback(
		() => queryClient.fetchQuery(auditEventTypesQuery(audit)),
		[queryClient, audit],
	);
	const authoring = useMemo(
		() => createDashboardAuthoring({ audit, readEventTypes, orgId, commit }),
		[audit, readEventTypes, orgId, commit],
	);
	return { authoring, draft };
}
