"use client";

import {
	Badge,
	Banner,
	Button,
	Card,
	List,
	ListItem,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import {
	QueryClient,
	QueryClientProvider,
	useMutation,
	useQueryClient,
} from "@tanstack/react-query";
import { useMemo, useState } from "react";
import {
	type ApiSourceSpec,
	apiSourceCollection,
	apiSourceName,
	apiSourceRefusal,
	matchPersonalApiSources,
	sourceLinks,
} from "./api-source.js";
import {
	beginAccountLinkHere,
	DatasourceAccountLinks,
	useMyAccountLinks,
} from "./directory.js";
import { messageOf } from "./errors.js";
import { fieldErrorClass } from "./credential-mode.js";
import { createDatasourceClient, type GatewayBinding } from "./gateway.js";
import { useListSources } from "./queries.js";
import type {
	DatasourceClient,
	DatasourceView,
	SourceOperationDeclaration,
} from "./types.js";
import { cn } from "./util.js";

interface ApiSourceCardBaseProps {
	orgId: string;
	/**
	 * The API the surface connects the person to. Read, never edited: the base
	 * URL is the surface's statement about what it calls, so the card renders
	 * it and submits it, and offers no control that could change it.
	 */
	api: ApiSourceSpec;
	/**
	 * The operations to declare on the person's source before they sign in.
	 * Absent declares nothing, and leaves any declared set as it is.
	 */
	operations?: readonly SourceOperationDeclaration[];
	className?: string;
}

/**
 * One API a person connects over OAuth, in one card: the `api` counterpart of
 * `DeclaredSourceCard`. The surface fixes the API; the person supplies only
 * the sign-in.
 *
 * It finds the person's own `api` source for the base URL (the host lists no
 * other member's), and when there is none creates it with the
 * authorization-code grant — which the host makes personal to its creator —
 * then declares `operations` on it, if given, and sends the browser to the
 * provider to sign in for that source. The return is completed on this page by
 * the `DatasourceAccountLinks` it renders, which is where the person's sign-in
 * for the source is then listed.
 *
 * Mount it on the page the provider returns to, and do not mount a second
 * `DatasourceAccountLinks` beside it: both would redeem the one-time state.
 */
export type ApiSourceCardProps = ApiSourceCardBaseProps &
	(
		| { client: DatasourceClient; gateway?: never }
		| { gateway: GatewayBinding; client?: never }
	);

export function ApiSourceCard(props: ApiSourceCardProps) {
	if (props.gateway) {
		const { gateway, ...rest } = props;
		return <GatewayBoundCard gateway={gateway} {...rest} />;
	}
	const { client, ...rest } = props;
	return <ApiSourceCardView client={client} {...rest} />;
}

function GatewayBoundCard({
	gateway,
	...rest
}: ApiSourceCardBaseProps & { gateway: GatewayBinding }) {
	const { apiBase, getAccessToken, refreshAccessToken, contentResource } =
		gateway;
	const client = useMemo(
		() =>
			createDatasourceClient({
				apiBase,
				getAccessToken,
				refreshAccessToken,
				contentResource,
			}),
		[apiBase, getAccessToken, refreshAccessToken, contentResource],
	);
	const [queryClient] = useState(
		() => new QueryClient({ defaultOptions: { queries: { retry: 1 } } }),
	);
	return (
		<QueryClientProvider client={queryClient}>
			<ApiSourceCardView client={client} {...rest} />
		</QueryClientProvider>
	);
}

type ApiSourceState = "setup" | "signin" | "connected" | "ambiguous";

function ApiSourceCardView({
	client,
	orgId,
	api,
	operations,
	className,
}: ApiSourceCardBaseProps & { client: DatasourceClient }) {
	const name = apiSourceName(api);
	const cache = useQueryClient();
	const list = useListSources(client, orgId);
	const links = useMyAccountLinks(client, orgId);
	const { indicator, quiet } = useLoadingPhase(list.isLoading);
	const matches = useMemo(
		() => matchPersonalApiSources(api, list.data ?? []),
		[api, list.data],
	);
	const source = matches.length === 1 ? matches[0] : undefined;
	const signedIn = !!source && sourceLinks(links.data ?? [], source.id).length > 0;
	const state: ApiSourceState =
		matches.length > 1
			? "ambiguous"
			: !source
				? "setup"
				: signedIn
					? "connected"
					: "signin";

	// Both halves, or neither: a source created with no way to sign in for it
	// would be a source this card then shows as needing a sign-in it cannot give.
	const addPersonalApiSource = client.beginAccountLink
		? client.addPersonalApiSource?.bind(client)
		: undefined;
	const declareSourceOperations = client.declareSourceOperations?.bind(client);
	// Declared before every sign-in, not only after creation: a declaration
	// refused once (the person's role, a schema the host would not admit) would
	// otherwise never be retried, and the host replaces the set atomically, so
	// declaring an unchanged set again changes nothing.
	const declare = async (sourceId: string) => {
		if (!operations) return;
		if (!declareSourceOperations) {
			throw new Error("this client cannot declare a source's operations");
		}
		await declareSourceOperations(orgId, sourceId, operations);
	};
	const connect = useMutation({
		mutationFn: async () => {
			const created = await addPersonalApiSource!({
				orgId,
				baseUrl: api.baseUrl.trim(),
				resourcePath: (api.resourcePath ?? "").trim(),
				targetCollection: apiSourceCollection(api),
				...(api.boundaryNodeId ? { boundaryNodeId: api.boundaryNodeId } : {}),
			});
			// From here the source exists whatever happens next, so the list is
			// refreshed even on a failure below: the card then offers the sign-in
			// for it rather than creating a second one.
			try {
				await declare(created.id);
				await beginAccountLinkHere(client, orgId, "api", created.id);
			} catch (error) {
				await cache.invalidateQueries({ queryKey: ["datasources", orgId] });
				throw error;
			}
		},
	});

	return (
		<Card
			className={cn("space-y-4", className)}
			title={name}
			description="Connected with your own sign-in, and personal to you."
			// Whether a source is signed in is a second read; until it answers, a
			// source that exists is not called one that needs a sign-in.
			actions={
				list.isSuccess && (!source || links.isSuccess) ? (
					<StateBadge state={state} />
				) : null
			}
		>
			<p className="type-body">
				<span className="font-mono">{api.baseUrl.trim()}</span>
			</p>
			{connect.isError && (
				<p role="alert" className={fieldErrorClass}>
					Couldn&apos;t connect {name}: {apiSourceRefusal(connect.error, name)}
				</p>
			)}
			{list.isError ? (
				<p role="alert" className="type-body text-destructive">
					Couldn&apos;t read your sources, so whether {name} is connected is
					unknown. {messageOf(list.error)}
				</p>
			) : !list.isSuccess ? (
				quiet || !indicator ? null : (
					<p role="status" className="type-body text-muted-foreground">
						Checking this API…
					</p>
				)
			) : state === "ambiguous" ? (
				<AmbiguousMatches name={name} matches={matches} />
			) : !source ? (
				addPersonalApiSource ? (
					<div className="space-y-2">
						<p className="type-body text-muted-foreground">
							Connecting creates a source for {name} that only you can use, and
							sends you to {name} to sign in. You come back here; there is
							nothing else to choose.
						</p>
						<Button
							type="button"
							disabled={connect.isPending}
							aria-busy={connect.isPending}
							onClick={() => connect.mutate()}
						>
							{connect.isPending ? `Opening ${name}…` : `Connect ${name}`}
						</Button>
					</div>
				) : (
					<p className="type-body text-muted-foreground">
						This client cannot connect an API.
					</p>
				)
			) : (
				<DatasourceAccountLinks
					client={client}
					orgId={orgId}
					connector="api"
					providerName={name}
					sourceId={source.id}
					{...(operations ? { beforeLink: () => declare(source.id) } : {})}
				/>
			)}
		</Card>
	);
}

function StateBadge({ state }: { state: ApiSourceState }) {
	if (state === "connected")
		return (
			<Badge tone="success" dot>
				Connected
			</Badge>
		);
	if (state === "signin")
		return (
			<Badge tone="warning" dot>
				Sign-in needed
			</Badge>
		);
	if (state === "ambiguous")
		return (
			<Badge tone="warning" dot>
				More than one source
			</Badge>
		);
	return (
		<Badge tone="neutral" dot>
			Set up
		</Badge>
	);
}

/**
 * Two of the person's sources serve one API. Reported rather than resolved,
 * as `DeclaredSourceCard` does: each holds its own sign-in and its own
 * declarations, and picking one would leave the other in use unseen.
 */
function AmbiguousMatches({
	name,
	matches,
}: {
	name: string;
	matches: DatasourceView[];
}) {
	return (
		<Banner tone="warning" title={`More than one source connects ${name}`}>
			<p>
				{`You have ${matches.length} sources for this API, so this card cannot say which one to use. Remove the ones you do not need in Admin → Data sources.`}
			</p>
			<List label="Matching sources">
				{matches.map((match) => (
					<ListItem key={match.id} className="font-mono">
						{`${match.id} · ${match.status}`}
					</ListItem>
				))}
			</List>
		</Banner>
	);
}
