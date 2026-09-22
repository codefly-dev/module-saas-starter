import { UserDetailPage } from "@/features/users/ui/user-detail-page";

export default async function Page({
	params,
}: {
	params: Promise<{ userId: string }>;
}) {
	const { userId } = await params;
	return <UserDetailPage userId={userId} />;
}
