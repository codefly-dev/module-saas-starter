import { SolutionsHomeSection } from "@/solutions/SolutionsMenu";

import DashboardClient from "./dashboard-client";

export default function DashboardPage() {
	return (
		<div className="flex flex-col gap-6">
			<SolutionsHomeSection />
			<DashboardClient />
		</div>
	);
}
