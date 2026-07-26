import { DashboardRouteShell } from "@/components/dashboard-route-shell";

export default function DashboardLayout({
	children,
}: {
	children: React.ReactNode;
}) {
	return <DashboardRouteShell>{children}</DashboardRouteShell>;
}
