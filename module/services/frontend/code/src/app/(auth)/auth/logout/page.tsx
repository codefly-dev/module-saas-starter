"use client";

import { useEffect } from "react";
import { useAuth } from "@/lib/auth";

// The proxy has always treated /auth/logout as public, but no page served it,
// so a user whose account menu was unreachable had no way to end the session.
// Opening this URL signs out and returns to the login page.
export default function Page() {
	const { logout } = useAuth();
	useEffect(() => {
		let cancelled = false;
		logout().finally(() => {
			if (!cancelled) window.location.replace("/auth/login");
		});
		return () => {
			cancelled = true;
		};
	}, [logout]);
	return (
		<p className="min-h-screen flex items-center justify-center">
			Signing out…
		</p>
	);
}
