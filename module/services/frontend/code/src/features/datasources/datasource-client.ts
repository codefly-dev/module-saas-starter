import {
	datasourceClientOverTransport,
	type DatasourceClient,
	type CollectionAccessView,
} from "@codefly-dev/saas-ui";
import { createClient } from "@connectrpc/connect";
import {
	PermissionService,
	PrincipalService,
} from "@/gen/saas/accounts/v1/authorization_pb";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import { OrganizationService } from "@/gen/saas/accounts/v1/organizations_pb";
import { TeamService } from "@/gen/saas/accounts/v1/teams_pb";
import { apiTransport } from "@/lib/connect/transport";

// The permission resource this deployment's collection content is governed by,
// declared by the composition. The host holds no domain content, so it has no
// noun of its own to fall back on: unset means undeclared, and every read and
// grant below refuses rather than guessing. It mirrors the `resources` a module
// declares in MODULE_PRINCIPALS, which is what the server matches grants against.
// Read per call rather than at module load: `NEXT_PUBLIC_*` is inlined at build
// time either way, and a deployment that changes it should not need this module
// re-imported to take effect.
const contentResource = () =>
	process.env.NEXT_PUBLIC_COLLECTION_CONTENT_RESOURCE;

// One client per declared resource: the value is stable in a running deployment,
// so this rebuilds only if it actually changes.
let scoped: { resource?: string; client: DatasourceClient } | null = null;
const scopedClient = (): DatasourceClient => {
	const resource = contentResource();
	if (!scoped || scoped.resource !== resource) {
		scoped = {
			resource,
			client: datasourceClientOverTransport(apiTransport, resource),
		};
	}
	return scoped.client;
};

const permissions = createClient(PermissionService, apiTransport);
const organizations = createClient(OrganizationService, apiTransport);
const principals = createClient(PrincipalService, apiTransport);
const teams = createClient(TeamService, apiTransport);

export const datasourceClient: DatasourceClient = {
	...datasourceClientOverTransport(apiTransport),
	// Resolve the declared resource per call, not at module load, so a scope query
	// reflects the deployment's configuration rather than import order.
	listAccessibleScopes(orgId) {
		return scopedClient().listAccessibleScopes!(orgId);
	},
	async listCollections(orgId) {
		const collections: CollectionAccessView[] = [];
		let pageToken = "";
		do {
			const page = await permissions.listCollectionAccess({
				orgId,
				pageToken,
				pageSize: 100,
			});
			collections.push(
				...page.collections.map(({ node, readGrants }) => ({
					nodeId: node!.id,
					label: node!.label,
					scopePath: node!.scopePath,
					grants: readGrants.map(
						({ grant, subjectLabel, roleName, actorLabel }) => ({
							id: grant!.id,
							subjectId: grant!.subjectId,
							subjectKind:
								grant!.subjectKind === SubjectKind.TEAM
									? ("team" as const)
									: ("principal" as const),
							scopePath: grant!.scopePath,
							roleId: grant!.roleId,
							subjectLabel,
							roleName,
							actorLabel,
						}),
					),
				})),
			);
			pageToken = page.nextPageToken;
		} while (pageToken);
		return collections;
	},
	async listGrantSubjects(orgId) {
		const [members, groups] = await Promise.all([
			organizations.listMembers({ orgId }),
			teams.listTeams({ orgId }),
		]);
		const names = new Map<string, string>();
		let pageToken = "";
		do {
			const page = await principals.listPrincipals({
				orgId,
				pageToken,
				pageSize: 100,
			});
			for (const principal of page.principals)
				names.set(principal.id, principal.displayName);
			pageToken = page.nextPageToken;
		} while (pageToken);
		return [
			...members.members.map((member) => ({
				id: member.userId,
				kind: "principal" as const,
				label: names.get(member.userId) || `Member ${member.userId}`,
			})),
			...groups.teams.map((team) => ({
				id: team.id,
				kind: "team" as const,
				label: `Team: ${team.name}`,
			})),
		];
	},
	async grantCollectionRead(orgId, scopePath, subject) {
		const resource = contentResource();
		if (!resource) {
			throw new Error(
				"no collection content resource is declared for this deployment, so there is no read permission to grant",
			);
		}
		const { roles } = await permissions.listRoles({ orgId });
		let role = roles.find(
			(role) =>
				role.orgId === orgId &&
				role.permissions.length === 1 &&
				role.permissions[0].resource === resource &&
				role.permissions[0].action === "read",
		);
		if (!role) {
			({ role } = await permissions.createRole({
				orgId,
				name: "Collection reader",
				description: `Read ${resource} in explicitly granted collections`,
				permissions: [{ resource, action: "read" }],
			}));
		}
		await permissions.grantScope({
			orgId,
			scopePath,
			subjectId: subject.id,
			subjectKind:
				subject.kind === "team" ? SubjectKind.TEAM : SubjectKind.PRINCIPAL,
			roleId: role!.id,
		});
	},
	async revokeCollectionRead(orgId, grant) {
		await permissions.revokeScope({
			orgId,
			scopePath: grant.scopePath,
			subjectId: grant.subjectId,
			subjectKind:
				grant.subjectKind === "team" ? SubjectKind.TEAM : SubjectKind.PRINCIPAL,
			roleId: grant.roleId,
		});
	},
};
