"use client";

import { useMemo } from "react";
import { contributedServiceBindings } from "@/generated/frontend-contributions";
import { pageEntries } from "@/lib/plugins/presentation";
import { useFrontendConfig } from "@/lib/providers";
import { useHeldPages } from "@/solutions/SolutionsMenu";

/**
 * The entries of the pages the host bundles that this viewer holds a role on,
 * by plugin (useHeldPages, joined to the composition's service bindings and
 * the plugins' own navigation): presented to them, entry and route, as a
 * remote page is to its role holders.
 */
export function useHeldEntries(): ReadonlyMap<string, string> {
	const pages = useHeldPages();
	const { plugins } = useFrontendConfig();
	return useMemo(
		() => pageEntries(pages, contributedServiceBindings, plugins),
		[pages, plugins],
	);
}
