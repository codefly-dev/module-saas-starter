"use client";

import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

const offers = createClient(OfferService, apiTransport);

/**
 * Renders its children only for a target that declares installation roles,
 * the only target a Setup tab can answer for. A solution always does: the
 * shared authority core composes none without an installer role. A module
 * does only when it declares a page and roles; the authority's offer review
 * is the answer, and a module it refuses (one that declares no roles) shows
 * nothing.
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
	const { organizationId = "" } = useAuth();
	const review = useQuery({
		queryKey: ["solution-declares-roles", organizationId, target],
		queryFn: async () =>
			(await offers.reviewOffer({ organizationId, target })).roles.length > 0,
		enabled: kind === "module" && organizationId !== "",
		retry: false,
		refetchOnWindowFocus: false,
	});
	const declared = kind === "solution" || review.data === true;
	return declared ? <>{children}</> : null;
}
