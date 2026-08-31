import {
	DashboardEditor,
	dashboard,
	Layout,
	USER_DASHBOARD_DRAFT_KEY,
} from "@/features/dashboard";

// The starting draft for a fresh browser: a titled, empty dashboard the user
// fills in from the editor. Persistence is per-browser (localStorage) until the
// server-backed store lands; the key namespaces this draft so it survives a
// reload without colliding with the reference pages.
const initial = dashboard({
	title: "My dashboard",
	description: "Build it from live audit events — no code, no deploy.",
	metrics: [],
});

export default function DashboardsPage() {
	return (
		<Layout
			title="Dashboards"
			description="Add, remove, and reorder widgets against your live audit trail."
		>
			<DashboardEditor storageKey={USER_DASHBOARD_DRAFT_KEY} initial={initial} />
		</Layout>
	);
}
