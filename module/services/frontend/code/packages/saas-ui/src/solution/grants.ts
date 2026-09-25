import { AccessibleScopeService } from "@codefly-dev/saas-sdk";
import { createClient } from "@connectrpc/connect";
import { useEffect, useState } from "react";
import type { SolutionRequestBinding } from "./binding.js";
import { solutionTransport } from "./transport.js";

/** Whether the viewer holds an action on a resource type anywhere in the org. */
export type AccessibleScopeState = "loading" | "error" | "none" | "some";

/**
 * Whether the viewer holds `action` on `resourceType` in `orgId`, from the
 * host's own accessible-scope API. It decides what a page offers — a notice
 * instead of a question nobody could answer — and authorizes nothing: every
 * read is authorized again by its owner.
 */
export function useAccessibleScope(
	binding: SolutionRequestBinding,
	orgId: string,
	resourceType: string,
	action: string,
): AccessibleScopeState {
	const [state, setState] = useState<AccessibleScopeState>("loading");
	const { apiBase, getAccessToken, authedFetch } = binding;
	useEffect(() => {
		if (!orgId) return;
		let live = true;
		setState("loading");
		createClient(
			AccessibleScopeService,
			solutionTransport({ apiBase, getAccessToken, authedFetch }),
		)
			.listMyAccessibleScopes({ orgId, resourceType, action, pageSize: 1 })
			.then((page) => {
				if (live) setState(page.scopes.length > 0 ? "some" : "none");
			})
			.catch(() => {
				if (live) setState("error");
			});
		return () => {
			live = false;
		};
	}, [apiBase, getAccessToken, authedFetch, orgId, resourceType, action]);
	return state;
}
