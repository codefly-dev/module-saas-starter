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

const permissions = createClient(PermissionService, apiTransport);
const organizations = createClient(OrganizationService, apiTransport);
const principals = createClient(PrincipalService, apiTransport);
const teams = createClient(TeamService, apiTransport);

export const datasourceClient: DatasourceClient = {
	...datasourceClientOverTransport(apiTransport),
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
		const { roles } = await permissions.listRoles({ orgId });
		let role = roles.find(
			(role) =>
				role.orgId === orgId &&
				role.permissions.length === 1 &&
				role.permissions[0].resource === "documents" &&
				role.permissions[0].action === "read",
		);
		if (!role) {
			({ role } = await permissions.createRole({
				orgId,
				name: "Collection reader",
				description: "Read documents in explicitly granted collections",
				permissions: [{ resource: "documents", action: "read" }],
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
