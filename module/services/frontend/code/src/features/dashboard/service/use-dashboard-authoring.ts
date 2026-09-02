"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { auditEventTypesQuery } from "@/features/audit/service/queries";
import { useAuth } from "@/lib/auth";
import { useAuditService } from "@/lib/hooks/use-api-client";
import type { DashboardDef } from "../model/schema";
import { createDashboardAuthoring, type DashboardAuthoring } from "./authoring";
import type { DashboardDraftStore } from "./draft-store";
import { type DashboardDraft, useDashboardDraft } from "./use-dashboard-draft";

// The base localStorage key the viewer's own dashboard draft persists under. It
// is the single symbol every host surface that renders or drives the user's
// dashboard binds, so those surfaces share one draft instead of each hardcoding
// a string. The Dashboards editor and the external-driver channel both scope it
// through scopedDashboardDraftKey below.
export const USER_DASHBOARD_DRAFT_KEY = "dashboard:draft";

// The base localStorage key the viewer's dashboard collection persists under,
// scoped per viewer/org through scopedDashboardDraftKey like the draft key.
export const USER_DASHBOARD_LIBRARY_KEY = "dashboard:library";

// Scope a base draft key to the viewer and their org. The draft lives in
// per-browser localStorage, so an unscoped key would restore one user's (or one
// org's) widgets under another's session on a shared browser. Every surface that
// binds the same draft MUST derive its key here, so an external driver's commit
// and the canvas that renders it resolve to the identical localStorage entry.
export function scopedDashboardDraftKey(
	base: string,
	viewer: { organizationId?: string | null; userId?: string | null },
): string {
	return `${base}:${viewer.organizationId ?? "none"}:${viewer.userId ?? "anon"}`;
}

// useDashboardAuthoring binds the driver-facing contract to the live audit
// client, the viewer's organization, and the canonical draft (useDashboardDraft
// from #317). setDashboard commits through the draft's setSpec, so a validated
// spec both persists and re-renders the mounted canvas; the returned draft is
// what a host renders and what surfaces a rejected persist.
export function useDashboardAuthoring(
	storageKey: string,
	initial: DashboardDef,
	store?: DashboardDraftStore,
): { authoring: DashboardAuthoring; draft: DashboardDraft } {
	const audit = useAuditService();
	const queryClient = useQueryClient();
	const { organizationId } = useAuth();
	const orgId = organizationId ?? "";
	const draft = useDashboardDraft(storageKey, initial, store);
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
