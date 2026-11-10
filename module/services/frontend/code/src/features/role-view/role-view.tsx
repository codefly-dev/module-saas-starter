"use client";

import { createClient } from "@connectrpc/connect";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { InstallationService } from "@/gen/host/installation/v1/installation_pb";
import { type RoleViewEntry, SessionService } from "@/gen/host/session/v1/session_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

const installations = createClient(InstallationService, apiTransport);
const sessions = createClient(SessionService, apiTransport);

/** A role the person can act as on one installation, by the roles they hold now. */
export interface Held {
	id: string;
	target: string;
	roles: string[];
}

/**
 * What the host says this session views, read from the host and not kept here:
 * the authority recorded the view, so the answer follows the token the browser
 * holds. A token that lapsed back to the person's own session says so.
 */
export function useRoleView(): RoleViewEntry[] {
	const { accessToken, isAuthenticated } = useAuth();
	const [view, setView] = useState<RoleViewEntry[]>([]);
	useEffect(() => {
		if (!isAuthenticated || !accessToken) return setView([]);
		let current = true;
		sessions
			.current({})
			.then((s) => current && setView(s.roleView))
			.catch(() => current && setView([]));
		return () => {
			current = false;
		};
	}, [accessToken, isAuthenticated]);
	return view;
}

/** "View as": what the person holds on each installation, and a way to act as less of it. */
export function RoleViewSelector() {
	const { organizationId, accessToken, enterRoleView, exitRoleView } = useAuth();
	const view = useRoleView();
	const [held, setHeld] = useState<Held[]>([]);
	useEffect(() => {
		if (!organizationId || !accessToken) return setHeld([]);
		let current = true;
		installations
			.listHeld({ organizationId })
			.then((r) => current && setHeld(r.installations.map((h) => ({ id: h.installation?.id ?? "", target: h.installation?.target ?? "", roles: h.roles }))))
			.catch(() => current && setHeld([]));
		return () => {
			current = false;
		};
	}, [organizationId, accessToken]);

	const open = useCallback(
		async (installationId: string, roles: string[]) => {
			try {
				const opened = await sessions.openRoleView({ installationId, roles });
				enterRoleView(opened.bearer);
				toast.success(`Viewing as ${roles.join(", ")}`);
			} catch (error) {
				toast.error(error instanceof Error ? error.message : "Could not open the role view.");
			}
		},
		[enterRoleView],
	);

	const choices = held.filter((h) => h.roles.length > 1 || view.some((v) => v.installationId === h.id));
	if (choices.length === 0) return null;
	return (
		<div aria-label="View as" className="flex flex-col gap-2 text-sm">
			{choices.map((h) => (
				<div key={h.id} className="flex flex-wrap items-center gap-2">
					<span className="font-medium">{h.target}</span>
					{h.roles.length > 1 && (
						<button type="button" className="underline" onClick={() => void open(h.id, h.roles)}>
							all of mine
						</button>
					)}
					{h.roles.map((role) => (
						<button key={role} type="button" className="underline" onClick={() => void open(h.id, [role])}>
							{role}
						</button>
					))}
				</div>
			))}
			{view.length > 0 && (
				<button type="button" className="text-left underline" onClick={() => void exitRoleView().catch((e: unknown) => toast.error(e instanceof Error ? e.message : "Could not restore your session."))}>
					Back to all my roles
				</button>
			)}
		</div>
	);
}

/** The bar that says, whatever the page shows, that this session acts as less than the person holds. */
export function RoleViewBanner() {
	const { exitRoleView } = useAuth();
	const view = useRoleView();
	if (view.length === 0) return null;
	return (
		<div role="status" className="fixed bottom-0 left-0 right-0 z-[9999] border-t-2 border-sky-500 bg-sky-300 px-4 py-2 text-sm text-sky-950">
			<span className="font-bold">Role view</span> &mdash; acting as{" "}
			{view.map((v) => `${v.roles.join(", ")} on ${v.installationId}`).join("; ")}
			<button
				type="button"
				className="ml-3 rounded bg-sky-900 px-3 py-1 text-sky-50"
				onClick={() => void exitRoleView().catch((e: unknown) => toast.error(e instanceof Error ? e.message : "Could not restore your session."))}
			>
				Back to all my roles
			</button>
		</div>
	);
}
