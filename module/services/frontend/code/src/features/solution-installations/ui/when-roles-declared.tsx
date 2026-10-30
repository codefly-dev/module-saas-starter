"use client";

import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

const offers = createClient(OfferService, apiTransport);

/**
 * Whether a target declares installation roles. A solution always does: the
 * shared authority core composes none without an installer role. A module
 * does only when it declares a page and roles; the authority's offer review
 * is the answer, and a module it refuses (one that declares no roles) does
 * not. Undefined while a module's answer is still being read.
 */
export function useRolesDeclared(
	target: string,
	kind?: string,
): boolean | undefined {
	const { organizationId = "" } = useAuth();
	const review = useQuery({
		queryKey: ["solution-declares-roles", organizationId, target],
		queryFn: async () =>
			(await offers.reviewOffer({ organizationId, target })).roles.length > 0,
		enabled: kind === "module" && organizationId !== "",
		retry: false,
		refetchOnWindowFocus: false,
	});
	if (kind !== "module") return kind === "solution";
	if (review.isError) return false;
	return review.data;
}

/**
 * Renders its children only for a target that declares installation roles,
 * the only target a Setup tab can answer for (`useRolesDeclared`).
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
	return useRolesDeclared(target, kind) ? <>{children}</> : null;
}
