"use client";

import {
	type CastHost,
	CastingProvider,
	sessionCastHost,
} from "@codefly-dev/saas-ui/casting";
import { Code, ConnectError } from "@connectrpc/connect";
import { usePathname, useRouter } from "next/navigation";
import { type ReactNode, useMemo } from "react";
import { useTargetTitles } from "@/features/solution-installations/ui/target-name";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

/**
 * This host's team casting: the kit's controls (`@codefly-dev/saas-ui/casting`)
 * over this host's own session interface, through the generated client. A cast
 * the host opens hands back a bearer, which this app adopts as the tab's
 * narrowed session (enterRoleView); the way back is the person's own session
 * (exitRoleView). The host decides every cast; nothing here does. A cast never
 * opens on an Admin page, so one asked from there lands where the team starts.
 */
export function HostCasting({ children }: { children: ReactNode }) {
	const { isAuthenticated, user, organizationId, enterRoleView, exitRoleView } =
		useAuth();
	const titles = useTargetTitles();
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
		if (who) return adapter;
		const signedOut = () =>
			Promise.reject(new ConnectError("not signed in", Code.Unauthenticated));
		return { ...adapter, current: signedOut, listTeams: signedOut };
	}, [who, onAdmin, enterRoleView, exitRoleView, router]);
	return (
		<CastingProvider host={host} titleOf={(target) => titles.get(target)}>
			{children}
		</CastingProvider>
	);
}
