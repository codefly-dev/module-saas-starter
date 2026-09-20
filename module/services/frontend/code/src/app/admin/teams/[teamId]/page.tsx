import { TeamDetailPage } from "@/features/teams/ui/team-detail-page";

export default async function Page({
	params,
}: {
	params: Promise<{ teamId: string }>;
}) {
	const { teamId } = await params;
	return <TeamDetailPage teamId={teamId} />;
}
