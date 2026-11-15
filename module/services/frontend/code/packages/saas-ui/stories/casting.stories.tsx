import {
	CastBar,
	type CastHost,
	CastingProvider,
	type CastTeam,
	TeamSwitcher,
	ViewAsTeamButton,
} from "../src/casting/index.js";

export default { title: "SaaS UI/Team casting" };

const teams: CastTeam[] = [
	{
		id: "directory-admin",
		name: "Directory admins",
		holds: [
			{
				installationId: "i1",
				target: "directory",
				title: "directory",
				role: "admin",
			},
		],
	},
	{
		id: "directory-user",
		name: "Directory users",
		holds: [
			{
				installationId: "i1",
				target: "directory",
				title: "directory",
				role: "user",
			},
		],
	},
];

// A host answering as both real ones do; nothing leaves the story.
const host = (castTeamId: string): CastHost => ({
	current: async () => ({
		personId: "jane",
		personDisplayName: "Jane Doe",
		organizationId: "acme",
		castTeamId,
		roleView: [],
	}),
	listTeams: async () =>
		castTeamId ? teams.filter((t) => t.id === castTeamId) : teams,
	open: async () => undefined,
	back: async () => undefined,
});
const titleOf = (target: string) =>
	target === "directory" ? "Directory" : undefined;

export const OwnView = {
	render: () => (
		<CastingProvider host={host("")} titleOf={titleOf}>
			<ViewAsTeamButton team={teams[0]} />
			<TeamSwitcher />
		</CastingProvider>
	),
};

export const ViewingAsATeam = {
	render: () => (
		<CastingProvider host={host("directory-admin")} titleOf={titleOf}>
			<CastBar />
			<TeamSwitcher />
		</CastingProvider>
	),
};
