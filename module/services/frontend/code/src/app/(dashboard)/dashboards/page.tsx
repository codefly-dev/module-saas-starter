import { Layout, MyDashboards } from "@/features/dashboard";

export default function DashboardsPage() {
	return (
		<Layout
			title="Dashboards"
			description="Create, open, and share dashboards built from your live audit trail."
		>
			<MyDashboards />
		</Layout>
	);
}
