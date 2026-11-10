"use client";

import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

const offers = createClient(OfferService, apiTransport);

/**
 * Whether a target declares installation roles, with the read's own failure,
 * from the authority's review of the one offer a page is about: a solution
 * always does; a module does only when it declares a page and roles, which
 * the review states (`rolesDeclared`), and the authority's NotFound (a module
 * that declares no roles) means it does not. Any other failure is not an
 * answer: `declared` stays undefined and `error` says why, so a transient
 * refusal never passes a module page off as one without roles. A listing of
 * several targets never asks this per module: it reads the authority's
 * offers once (`useOfferedTargets`).
 */
export function useRolesDeclaredReview(
	target: string,
	kind?: string,
): { declared: boolean | undefined; error: unknown; retry: () => void } {
	const { organizationId = "" } = useAuth();
	const review = useQuery({
		queryKey: ["solution-declares-roles", organizationId, target],
		queryFn: async () =>
			(await offers.reviewOffer({ organizationId, target })).rolesDeclared,
		retry: false,
		refetchOnWindowFocus: false,
		enabled: kind === "module" && organizationId !== "",
	});
	const retry = () => void review.refetch();
	if (kind !== "module")
		return { declared: kind === "solution", error: undefined, retry };
	if (review.isError) {
		const declaresNone = ConnectError.from(review.error).code === Code.NotFound;
		return {
			declared: declaresNone ? false : undefined,
			error: declaresNone ? undefined : review.error,
			retry,
		};
	}
	return { declared: review.data, error: undefined, retry };
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
	return useRolesDeclaredReview(target, kind).declared ? children : null;
}
