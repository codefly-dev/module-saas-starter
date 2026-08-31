import { DashboardEditor, Layout } from "@/features/dashboard";

export default function DashboardsPage() {
	return (
		<Layout
			title="Dashboards"
			description="Compose a dashboard over your organization's audit trail — edits apply live and are saved in this browser."
		>
			<DashboardEditor />
		</Layout>
	);
}
