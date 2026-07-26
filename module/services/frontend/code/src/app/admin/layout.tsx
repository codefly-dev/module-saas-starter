import { AdminRouteShell } from "@/components/admin-route-shell";

export default function AdminRouteLayout({
	children,
}: {
	children: React.ReactNode;
}) {
	return <AdminRouteShell>{children}</AdminRouteShell>;
}
