import { TeamDetailsPage } from "@/features/teams/ui/team-details-page";

export default async function Page({
	params,
}: {
	params: Promise<{ teamId: string }>;
}) {
	const { teamId } = await params;
	return <TeamDetailsPage teamId={teamId} />;
}
