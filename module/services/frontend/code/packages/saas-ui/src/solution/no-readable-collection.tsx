/**
 * Where the host grants read access to a collection: its data-sources admin
 * page, whose "Collection access" section lists every collection and who may
 * read it. A path of the host that renders this kit, so it is same-origin for
 * every remote the host mounts.
 */
export const COLLECTION_ACCESS_PATH = "/admin/datasources";

export interface NoReadableCollectionProps {
	/**
	 * Whether the viewer can grant read access themselves — an organization
	 * administrator (see `viewerAdministersOrganization`). It decides only what
	 * the notice offers; the grant is authorized by the host.
	 */
	canGrant: boolean;
	/** What the page would have shown, ending the first sentence ("…, so there
	 *  are no documents to show."). Default "documents to show". */
	subject?: string;
	/** Where grants are made. Default {@link COLLECTION_ACCESS_PATH}. */
	grantsHref?: string;
	className?: string;
}

/**
 * What a solution shows a viewer who may read no collection: why the page is
 * empty and what to do about it. A member is told to ask an organization
 * administrator, and where that administrator makes the grant; an
 * administrator gets the link to make it. Connecting or syncing a source never
 * grants access, so the notice says a grant is what is missing.
 *
 * The caller decides the viewer holds no readable collection (for instance
 * `useAccessibleScope(…) === "none"`); this only says so.
 */
export function NoReadableCollection({
	canGrant,
	subject = "documents to show",
	grantsHref = COLLECTION_ACCESS_PATH,
	className,
}: NoReadableCollectionProps) {
	return (
		<div data-slot="no-readable-collection" className={className}>
			<p className="type-emphasis">
				You can’t read any collection yet, so there are no {subject}.
			</p>
			{canGrant ? (
				<p className="type-body text-muted-foreground">
					Connecting or syncing a source grants no read access; a grant does.{" "}
					<a
						href={grantsHref}
						className="text-primary underline underline-offset-2"
					>
						Grant read access to a collection
					</a>{" "}
					to yourself, a member or a team in Data sources → Collection access.
				</p>
			) : (
				<p className="type-body text-muted-foreground">
					Ask an organization administrator to grant you read access to a
					collection. Administrators grant it in Admin → Data sources →
					Collection access.
				</p>
			)}
		</div>
	);
}
