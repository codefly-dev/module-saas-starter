import type { Datasource } from "@/gen/saas/accounts/v1/datasource_pb";

/**
 * What to call one source on screen.
 *
 * A source has no name of its own: what identifies it to a person is whatever
 * its provider was pointed at, and each provider spells that differently. The
 * id is the fallback rather than the default — it identifies the row exactly
 * and tells a reader nothing.
 */
export function sourceLabel(source: Datasource): string {
	return (
		source.github?.repo ||
		source.api?.baseUrl ||
		source.crawler?.sitemapUrl ||
		source.upload?.bucket ||
		source.id
	);
}
