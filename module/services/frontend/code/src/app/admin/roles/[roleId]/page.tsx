import { RoleDetailPage } from "@/features/roles/ui/role-detail-page";

export default async function Page({
	params,
}: {
	params: Promise<{ roleId: string }>;
}) {
	const { roleId } = await params;
	return <RoleDetailPage roleId={roleId} />;
}
