import { SolutionSetup } from "../src/index.js";
import {
	changeRefusal,
	exampleOrganization,
	exampleTarget,
	outsiderRefusal,
	reviewedOffer,
	setupFixture,
} from "./setup-fixtures.js";

export default { title: "SaaS UI/Solution setup" };

// One component, one message shape: each story answers the same
// host.installation.v1 messages, shaped as the local authority answers them
// (a run that composes tool bindings) or as a hosted SaaS answers them (no
// tool bindings yet, and host conditions of its own).

const subjects = [
	{ kind: "person" as const, id: "person-jane", label: "jane@example.com" },
	{ kind: "team" as const, id: "team-finance", label: "Finance" },
];

function Story({
	fixture = setupFixture(),
	conditions,
}: {
	fixture?: ReturnType<typeof setupFixture>;
	conditions?: Parameters<typeof SolutionSetup>[0]["conditions"];
}) {
	return (
		<SolutionSetup
			transport={fixture.transport}
			organizationId={exampleOrganization}
			target={exampleTarget}
			subjects={subjects}
			conditions={conditions}
		/>
	);
}

/** Is it ready? — installed, as a local authority answers. */
export const ReadyLocalAuthority = {
	render: () => (
		<Story fixture={setupFixture({ offer: reviewedOffer({ tools: true }) })} />
	),
};
/** Is it ready? — the same answer on SaaS, with a condition only the host knows. */
export const ReadySaaS = {
	render: () => (
		<Story
			fixture={setupFixture({ offer: reviewedOffer({ tools: false }) })}
			conditions={[
				{
					key: "source:example/repo",
					heading: "A source has no active delegation",
					subject: "example/repo",
					detail: "The source has no active delegation to documents.",
					clearedBy: "You",
					action: {
						kind: "link",
						label: "Reconnect this source",
						href: "/admin/datasources",
					},
				},
			]}
		/>
	),
};
/** Is it ready? — not installed here yet. */
export const NotInstalled = {
	render: () => (
		<Story
			fixture={setupFixture({
				offer: reviewedOffer({ tools: true, installed: false }),
			})}
		/>
	),
};
/** Roles — operations and their agent tools, as a local run composes them. */
export const RolesLocalAuthority = {
	render: () => (
		<Story fixture={setupFixture({ offer: reviewedOffer({ tools: true }) })} />
	),
};
/** Roles — the same roles and operations on SaaS, which composes no tool binding. */
export const RolesSaaS = {
	render: () => (
		<Story fixture={setupFixture({ offer: reviewedOffer({ tools: false }) })} />
	),
};
/** Who holds them — an administrator grants and revokes (local authority). */
export const HoldersLocalAuthority = {
	render: () => (
		<Story fixture={setupFixture({ offer: reviewedOffer({ tools: true }) })} />
	),
};
/** Who holds them — the same on SaaS, the authority refusing a change with its reason. */
export const HoldersSaaSRefused = {
	render: () => (
		<Story
			fixture={setupFixture({
				offer: reviewedOffer({ tools: false }),
				refuse: { change: changeRefusal },
			})}
		/>
	),
};
/** A member holding a role: what it grants, read-only, no consent tuple. */
export const MemberReadOnly = {
	render: () => (
		<Story
			fixture={setupFixture({
				offer: reviewedOffer({ tools: true, administrator: false }),
				refuse: { grants: changeRefusal },
			})}
		/>
	),
};
/** Anyone else: the authority's one reason, in every section. */
export const Refused = {
	render: () => (
		<Story fixture={setupFixture({ refuse: { offer: outsiderRefusal } })} />
	),
};
