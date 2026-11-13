"use client";

import { useMemo } from "react";
import { contributedServiceBindings } from "@/generated/frontend-contributions";
import { pagePlugins } from "@/lib/plugins/presentation";
import { useHeldPages } from "@/solutions/SolutionsMenu";

/**
 * The plugins of the pages the host bundles that this viewer holds a role on
 * (useHeldPages, joined to the composition's service bindings): presented to
 * them, entry and route, as a remote page is to its role holders.
 */
export function useHeldPlugins(): ReadonlySet<string> {
	const pages = useHeldPages();
	return useMemo(() => pagePlugins(pages, contributedServiceBindings), [pages]);
}
