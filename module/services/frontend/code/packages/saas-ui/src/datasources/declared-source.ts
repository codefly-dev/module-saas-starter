import type { DatasourceView } from "./types.js";

/**
 * A source a solution declares it is built on, as its registration manifest
 * states it (`sources:`) and as the host hands it back to the mounted remote
 * (`SolutionBinding.declaredSources`).
 *
 * The point of a declaration is that the repository is NOT a question put to
 * the person. A solution that reads one known repository already knows which
 * one; all that is left to supply is the credential. So this type is read, it
 * is never edited by a surface, and nothing in it is defaulted from what the
 * person typed.
 *
 * It is not a permission and not a connection: declaring a source grants
 * nothing and connects nothing. It is only the statement "this is the source
 * my solution is built on", against which the org's connected sources can be
 * matched.
 */
export interface DeclaredSource {
	/** Only github today — the provider whose connector takes a repo + paths. */
	provider: "github";
	/** "owner/name", the same shape `AddGitHubSourceRequest.repo` takes. */
	repo: string;
	/** Repository-relative path prefixes. Empty or absent means the whole repo. */
	paths?: string[];
	/** Git ref to read. Absent resolves to the repository's default branch. */
	ref?: string;
	/**
	 * What to call this source where a person sees it, and — because a
	 * connected source has to land somewhere — the collection the host mints or
	 * reuses for its entries. Absent falls back to the repository name.
	 */
	label?: string;
}

/**
 * GitHub treats `owner/name` case-insensitively, so two sources whose repos
 * differ only in case are one repository and must match each other. Comparing
 * them literally would show a declaration as unconnected beside the very
 * source that serves it.
 */
function sameRepo(a: string, b: string): boolean {
	return a.toLowerCase() === b.toLowerCase();
}

/** Set equality over path prefixes; order is not meaningful in either place. */
function samePaths(declared: string[], connected: string[]): boolean {
	if (declared.length !== connected.length) return false;
	const remaining = new Set(connected);
	for (const path of declared) {
		if (!remaining.delete(path)) return false;
	}
	return true;
}

/**
 * Every connected source that serves a declaration: same provider, same
 * repository, and — only when the declaration names paths — the same set of
 * path prefixes. A declaration that names no paths matches the repository
 * however it was scoped, because it has asserted nothing about scope.
 *
 * Returns ALL of them rather than the first, deliberately. Two sources for one
 * declared repository is a real state an organization can reach (one connected
 * through the App, one through a PAT; one scoped to `docs/`, one to the whole
 * repo), and picking one silently would show its status, sync its content and
 * leave the other invisible — so a caller reports the ambiguity instead.
 */
export function matchDeclaredSources(
	declared: DeclaredSource,
	sources: readonly DatasourceView[],
): DatasourceView[] {
	const paths = declared.paths ?? [];
	return sources.filter(
		(source) =>
			source.provider === declared.provider &&
			sameRepo(source.repo, declared.repo) &&
			(paths.length === 0 || samePaths(paths, source.paths)),
	);
}

/** The collection a declared source's entries land in. */
export function declaredCollectionLabel(declared: DeclaredSource): string {
	return declared.label?.trim() || declared.repo;
}
