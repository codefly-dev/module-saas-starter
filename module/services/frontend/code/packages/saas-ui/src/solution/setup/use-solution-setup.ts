"use client";

import {
	HostInstallationService,
	HostOfferService,
	type ReviewedOffer,
	type RoleGrant,
} from "@codefly-dev/saas-sdk";
import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { useCallback, useEffect, useMemo, useState } from "react";
import { installed } from "./model.js";

/** The host-interface clients the setup reads and changes through. */
export interface SetupClients {
	offers: Client<typeof HostOfferService>;
	installations: Client<typeof HostInstallationService>;
}

export function setupClients(transport: Transport): SetupClients {
	return {
		offers: createClient(HostOfferService, transport),
		installations: createClient(HostInstallationService, transport),
	};
}

/**
 * One read of a solution's setup at one organization, every part the
 * authority's answer: the offer (the solution's declared roles, what they
 * reach, its installation there) and who holds which role. A part the
 * authority refused carries its refusal instead, so a page shows the reason
 * where the answer would have been.
 */
export type SetupState =
	| { phase: "loading" }
	| {
			phase: "ready";
			offer?: ReviewedOffer;
			offerError?: unknown;
			grants?: RoleGrant[];
			grantsError?: unknown;
	  };

export interface SolutionSetupRead {
	state: SetupState;
	clients: SetupClients;
	/** Reads everything again, keeping the current answer on screen meanwhile. */
	reload: () => void;
}

export function useSolutionSetup(
	transport: Transport,
	organizationId: string,
	target: string,
): SolutionSetupRead {
	const clients = useMemo(() => setupClients(transport), [transport]);
	const [epoch, setEpoch] = useState(0);
	const [state, setState] = useState<{ key: string; value: SetupState }>({
		key: "",
		value: { phase: "loading" },
	});
	const key = `${organizationId}\u0000${target}`;
	useEffect(() => {
		let live = true;
		void epoch;
		(async (): Promise<SetupState> => {
			const out: SetupState = { phase: "ready" };
			try {
				out.offer = await clients.offers.reviewOffer({
					organizationId,
					target,
				});
			} catch (error) {
				out.offerError = error;
				return out;
			}
			const installation = out.offer.installation;
			if (installation && installed(out.offer)) {
				try {
					const answer = await clients.installations.listRoleGrants({
						organizationId,
						installationId: installation.id,
					});
					out.grants = answer.grants;
				} catch (error) {
					out.grantsError = error;
				}
			}
			return out;
		})().then((value) => {
			if (live) setState({ key, value });
		});
		return () => {
			live = false;
		};
	}, [clients, organizationId, target, key, epoch]);
	const reload = useCallback(() => setEpoch((n) => n + 1), []);
	// An answer about another solution or organization is never shown as this one's.
	const value: SetupState =
		state.key === key ? state.value : { phase: "loading" };
	return { state: value, clients, reload };
}
