"use client";

import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { useQueries, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";

const offers = createClient(OfferService, apiTransport);

/** A target as the catalogue names it: its route alias and declared kind. */
export interface ReviewedTarget {
	routeAlias: string;
	kind: string;
}

/**
 * The one read of a module's declared roles. Every hook here asks it with
 * this key, so a page that reviewed a target to list it and a dialog that
 * reviews the same target to install it share one cached answer.
 */
function rolesDeclaredQuery(organizationId: string, target: string) {
	return {
		queryKey: ["solution-declares-roles", organizationId, target],
		queryFn: async () =>
			(await offers.reviewOffer({ organizationId, target })).roles.length > 0,
		retry: false,
		refetchOnWindowFocus: false,
	};
}

/**
 * What one review answers. Only the authority's NotFound (a module that
 * declares no roles) means no; any other failure is not an answer.
 */
function reviewAnswer(review: {
	isError: boolean;
	error: unknown;
	data: boolean | undefined;
}): { declared: boolean | undefined; error: unknown } {
	if (review.isError) {
		const declaresNone = ConnectError.from(review.error).code === Code.NotFound;
		return {
			declared: declaresNone ? false : undefined,
			error: declaresNone ? undefined : review.error,
		};
	}
	return { declared: review.data, error: undefined };
}

/**
 * Whether a target declares installation roles, with the read's own failure.
 * A solution always does: the shared authority core composes none without an
 * installer role. A module does only when it declares a page and roles; the
 * authority's offer review is the answer, and only its NotFound (a module
 * that declares no roles) means it does not. Any other failure is not an
 * answer: `declared` stays undefined and `error` says why, so a transient
 * refusal never passes a module page off as one without roles.
 */
export function useRolesDeclaredReview(
	target: string,
	kind?: string,
): { declared: boolean | undefined; error: unknown; retry: () => void } {
	const { organizationId = "" } = useAuth();
	const review = useQuery({
		...rolesDeclaredQuery(organizationId, target),
		enabled: kind === "module" && organizationId !== "",
	});
	const retry = () => void review.refetch();
	if (kind !== "module")
		return { declared: kind === "solution", error: undefined, retry };
	return { ...reviewAnswer(review), retry };
}

/**
 * Which of several targets declare installation roles, by route alias: a
 * solution always, a module by the same review `useRolesDeclaredReview`
 * makes, one per module. A module whose answer is not in yet, or whose read
 * failed, is undefined — never "declares none" — and `error` is the first
 * such failure so a list built on this can say what it could not read.
 */
export function useRolesDeclaredFor(
	targets: ReviewedTarget[],
	enabled = true,
): {
	declared: Record<string, boolean | undefined>;
	error: unknown;
	retry: () => void;
} {
	const { organizationId = "" } = useAuth();
	// One review per module, however many rows name it.
	const modules = [
		...new Map(
			targets
				.filter((target) => target.kind === "module")
				.map((target) => [target.routeAlias, target]),
		).values(),
	];
	const reviews = useQueries({
		queries: modules.map((target) => ({
			...rolesDeclaredQuery(organizationId, target.routeAlias),
			enabled: enabled && organizationId !== "",
		})),
	});
	const declared: Record<string, boolean | undefined> = {};
	const unanswered: typeof reviews = [];
	for (const target of targets)
		if (target.kind === "solution") declared[target.routeAlias] = true;
	modules.forEach((target, index) => {
		const answer = reviewAnswer(reviews[index]);
		declared[target.routeAlias] = answer.declared;
		if (answer.error !== undefined) unanswered.push(reviews[index]);
	});
	return {
		declared,
		error: unanswered[0]?.error,
		// Only a read that gave no answer is asked again: NotFound is an answer.
		retry: () => {
			for (const review of unanswered) void review.refetch();
		},
	};
}

/** Whether a target declares installation roles; undefined while unknown. */
export function useRolesDeclared(
	target: string,
	kind?: string,
): boolean | undefined {
	return useRolesDeclaredReview(target, kind).declared;
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
