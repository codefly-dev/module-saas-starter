"use client";

import {
	type CastHost,
	CastingProvider,
	type CastSession,
	sessionCastHost,
} from "@codefly-dev/saas-ui/casting";
import { Code, ConnectError } from "@connectrpc/connect";
import { usePathname, useRouter } from "next/navigation";
import { type ReactNode, useCallback, useMemo } from "react";
import { useTargetTitles } from "@/features/solution-installations/ui/target-name";
import type { Session } from "@/gen/host/session/v1/session_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

/**
 * The kit's read of the session, answered from the auth provider's: the first
 * is the read the provider made for the installed token, and each one after it
 * asks the host anew (the kit reads again when what it holds has ended).
 */
function sessionReader(
	hostSession: (again?: boolean) => Promise<Session>,
): () => Promise<CastSession> {
	let asked = false;
	return async () => {
		const again = asked;
		asked = true;
		const session = await hostSession(again);
		return {
			personId: session.personId,
			personDisplayName: session.personDisplayName,
			organizationId: session.organizationId,
			castTeamId: session.castTeamId,
			roleView: session.roleView.map((view) => ({
				installationId: view.installationId,
				roles: [...view.roles],
			})),
			expiresAtUnix: Number(session.expiresAtUnix ?? 0),
		};
	};
}

/**
 * This host's team casting: the kit's controls (`@codefly-dev/saas-ui/casting`)
 * over this host's own session interface, through the generated client. A cast
 * the host opens hands back a bearer, which this app adopts as the tab's
 * narrowed session (enterRoleView); the way back is the person's own session
 * (exitRoleView). The host decides every cast; nothing here does. A cast never
 * opens on an Admin page, so one asked from there lands where the team starts.
 *
 * The session itself is not asked of the host a second time. The auth provider
 * reads it for every installed token (`hostSession`), and the kit's first read
 * here is that same answer; the kit's later reads, made when the answer it
 * holds has reached its stated end, ask the host anew.
 */
export function HostCasting({ children }: { children: ReactNode }) {
	const {
		isAuthenticated,
		user,
		organizationId,
		enterRoleView,
		exitRoleView,
		hostSession,
	} = useAuth();
	const titles = useTargetTitles();
	// One function per menu: the provider re-names what it keeps when this changes.
	const titleOf = useCallback((target: string) => titles.get(target), [titles]);
	const router = useRouter();
	// A cast never opens on an Admin page: one asked from there lands at "/".
	const onAdmin = (usePathname() ?? "").startsWith("/admin");
	// Who is signed in, where: a new one is a new host, and the session is read again.
	const who = isAuthenticated && user?.id ? `${user.id}@${organizationId}` : "";
	const host = useMemo<CastHost>(() => {
		const adapter = sessionCastHost({
			transport: apiTransport,
			adopt: (cast, team) => {
				enterRoleView(cast.bearer, `team ${team.name}`);
				if (onAdmin) router.push("/");
			},
			back: exitRoleView,
		});
		if (who) return { ...adapter, current: sessionReader(hostSession) };
		const signedOut = () =>
			Promise.reject(new ConnectError("not signed in", Code.Unauthenticated));
		return { ...adapter, current: signedOut, listTeams: signedOut };
	}, [who, onAdmin, enterRoleView, exitRoleView, hostSession, router]);
	return (
		<CastingProvider host={host} titleOf={titleOf}>
			{children}
		</CastingProvider>
	);
}
