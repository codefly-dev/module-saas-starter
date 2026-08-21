import { SolutionsMenu } from "@/solutions/SolutionsMenu";

import DashboardClient from "./dashboard-client";

export default function DashboardPage() {
	return (
		<div className="flex flex-col gap-6">
			<section className="flex flex-col gap-3">
				<h2 className="text-sm font-semibold uppercase tracking-wide opacity-60">
					Solutions
				</h2>
				<SolutionsMenu variant="cards" />
			</section>
			<DashboardClient />
		</div>
	);
}
