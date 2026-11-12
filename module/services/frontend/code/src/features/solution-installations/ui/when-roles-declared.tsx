"use client";

import type { ReactNode } from "react";
import { useAuth } from "@/lib/auth";
import { hostRefusal, isModulePage } from "../model/installations";
import { useAvailableSolutions } from "../service/queries";

/**
 * Whether a target declares installation roles, with the read's own failure.
 * A solution always does. A module does only when the host's catalogue row
 * for it carries roles (`isModulePage`), the same read the Solutions page
 * classifies its module pages by. The offer review is not asked here: for a
 * module it answers NotFound ("not a composed solution; a module is
 * installed by composition"), a refusal that says nothing about roles. A
 * failed catalogue read is no answer: `declared` stays undefined and `error`
 * says why, so a transient failure never passes a module page off as one
 * without roles.
 */
export function useRolesDeclaredReview(
	target: string,
	kind?: string,
): { declared: boolean | undefined; error: unknown; retry: () => void } {
	const { organizationId = "" } = useAuth();
	const available = useAvailableSolutions(
		kind === "module" ? organizationId : "",
	);
	const retry = () => void available.refetch();
	if (kind !== "module")
		return { declared: kind === "solution", error: undefined, retry };
	if (available.isError)
		return { declared: undefined, error: available.error, retry };
	if (!available.data) return { declared: undefined, error: undefined, retry };
	return {
		declared: available.data.some(
			(row) => row.routeAlias === target && isModulePage(row),
		),
		error: undefined,
		retry,
	};
}

/**
 * Renders its children only for a target that declares installation roles,
 * the only target a Setup tab can answer for (`useRolesDeclaredReview`). A
 * failed read is said, in the host's words, never passed off as "none".
 */
export function WhenRolesDeclared({
	target,
	kind,
	children,
}: {
	target: string;
	kind?: "module" | "solution";
	children: ReactNode;
}) {
	const { declared, error } = useRolesDeclaredReview(target, kind);
	if (error)
		return (
			<p role="alert" className="text-sm text-muted-foreground">
				Could not read whether this page has roles to set up:{" "}
				{hostRefusal(error)}
			</p>
		);
	return declared ? children : null;
}
