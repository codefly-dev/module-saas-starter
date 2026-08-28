"use client";

import { useMemo } from "react";
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
	const { organizationId } = useAuth();
	const orgId = organizationId ?? "";
	const draft = useDashboardDraft(storageKey, initial);
	const commit = draft.setSpec;
	const authoring = useMemo(
		() => createDashboardAuthoring({ audit, orgId, commit }),
		[audit, orgId, commit],
	);
	return { authoring, draft };
}
