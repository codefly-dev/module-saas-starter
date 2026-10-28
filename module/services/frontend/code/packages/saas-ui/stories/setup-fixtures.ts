import {
	HostInstallationService,
	HostInstallationStatus,
	HostOfferService,
} from "@codefly-dev/saas-sdk";
import {
	Code,
	ConnectError,
	createRouterTransport,
	type Transport,
} from "@connectrpc/connect";

/**
 * The host interfaces as a host answers them, in memory and over the real
 * protobuf wire: the setup component cannot tell this from a host, which is
 * the point. A local authority and SaaS serve the SAME messages; they differ
 * only in what they know (a local run composes tool bindings, SaaS reports
 * host conditions), so one fixture builds both shapes.
 */

const scope = (resourceKind: string, ...actions: string[]) => ({
	resourceKind,
	actions,
	attachedOnly: false,
	resourceIds: [] as string[],
});

export const exampleTarget = "example-pages";

export const exampleOrganization = "org-acme";

const installation = {
	id: "installation-1",
	organizationId: "org-acme",
	target: exampleTarget,
	status: HostInstallationStatus.ACTIVE,
	revision: BigInt(4),
	role: "viewer",
	roles: ["editor", "viewer"],
};

/**
 * A reviewed offer. `tools` says whether the host composes tool bindings (a
 * local run does; SaaS does not yet), `administrator` whether the reader is
 * answered the consent tuple (a member holding a role is not).
 */
export function reviewedOffer({
	tools,
	installed = true,
	administrator = true,
}: {
	tools: boolean;
	installed?: boolean;
	administrator?: boolean;
}) {
	const tool = (name: string, binding: string, write: boolean) =>
		tools ? [{ name, binding, write }] : [];
	return {
		target: exampleTarget,
		toolsComposed: tools,
		approval: administrator
			? {
					targetId: exampleTarget,
					generation: BigInt(3),
					expectedRevision: installed ? BigInt(4) : BigInt(0),
					digest: "sha256:example",
				}
			: undefined,
		installation: installed ? installation : undefined,
		ceiling: [
			{
				binding: "documents",
				module: "documents",
				scopes: [scope("example-records", "read", "write")],
			},
		],
		roles: [
			{
				name: "editor",
				scopes: [scope("example-pages.page", "read", "write")],
				capabilities: [
					{
						module: exampleTarget,
						operation: "GET /items/{id}",
						requires: [scope("example-pages.page", "read")],
						tools: tool("read_item", "EXAMPLE_PAGES__API_CONNECT_READ", false),
					},
					{
						module: exampleTarget,
						operation: "POST /items/{id}",
						requires: [scope("example-pages.page", "write")],
						tools: tool("write_item", "EXAMPLE_PAGES__API_CONNECT_WRITE", true),
					},
				],
			},
			{
				name: "viewer",
				scopes: [scope("example-pages.page", "read")],
				capabilities: [
					{
						module: exampleTarget,
						operation: "GET /items/{id}",
						requires: [scope("example-pages.page", "read")],
						tools: tool("read_item", "EXAMPLE_PAGES__API_CONNECT_READ", false),
					},
				],
			},
		],
	};
}

export interface SetupFixture {
	transport: Transport;
	/** Every change the page asked for, in order. */
	calls: { method: string; request: unknown }[];
}

/**
 * A host over one offer. `refuse` names the answer the authority refuses with
 * its reason, as a member or another organization's administrator is refused.
 */
export function setupFixture({
	offer = reviewedOffer({ tools: true }),
	grants = [
		{ role: "viewer", personId: "", teamId: "team-finance" },
		{ role: "editor", personId: "person-jane", teamId: "" },
	],
	refuse = {},
}: {
	offer?: ReturnType<typeof reviewedOffer>;
	grants?: { role: string; personId: string; teamId: string }[];
	refuse?: Partial<Record<"offer" | "grants" | "change", ConnectError>>;
} = {}): SetupFixture {
	const calls: SetupFixture["calls"] = [];
	let held = [...grants];
	const transport = createRouterTransport(({ service }) => {
		service(HostOfferService, {
			reviewOffer() {
				if (refuse.offer) throw refuse.offer;
				return offer;
			},
		});
		service(HostInstallationService, {
			listRoleGrants() {
				if (refuse.grants) throw refuse.grants;
				return { grants: held };
			},
			grantRole(request) {
				calls.push({ method: "GrantRole", request });
				if (refuse.change) throw refuse.change;
				held = [
					...held,
					{
						role: request.role,
						personId: request.personId,
						teamId: request.teamId,
					},
				];
				return installation;
			},
			revokeRole(request) {
				calls.push({ method: "RevokeRole", request });
				if (refuse.change) throw refuse.change;
				held = held.filter(
					(g) =>
						!(
							g.role === request.role &&
							g.personId === request.personId &&
							g.teamId === request.teamId
						),
				);
				return installation;
			},
		});
	});
	return { transport, calls };
}

/** The authority's refusal of someone holding no role, in its own words. */
export const outsiderRefusal = new ConnectError(
	'reviewing "example-pages" requires an administrator or owner of organization "org-acme", or a role on its installation there',
	Code.PermissionDenied,
);

/** The authority's refusal of a role change by someone who may not make it. */
export const changeRefusal = new ConnectError(
	'installation administration requires an administrator or owner of organization "org-acme"',
	Code.PermissionDenied,
);
