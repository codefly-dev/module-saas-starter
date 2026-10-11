"use client";

import { useCallback, useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
import { Button } from "@/shared/ui";

// The proxy has always treated /auth/logout as public, but no page served it,
// so a user whose account menu was unreachable had no way to end the session.
// Opening the URL signs out and returns to the login page.
//
// An INCOMPLETE sign-out stops here instead of redirecting. The server answers
// 503 when it could not record the revocation, which leaves an already-issued
// access token valid until it expires; redirecting to the login page would
// present that as a finished sign-out, which is the one answer a sign-out must
// never give. The refresh cookie is kept in that case, so the retry below still
// has the credential it needs.
export function LogoutPage() {
	const { logout } = useAuth();
	const [incomplete, setIncomplete] = useState(false);

	const signOut = useCallback(async () => {
		setIncomplete(false);
		try {
			const { complete } = await logout();
			if (complete) {
				window.location.replace("/auth/login");
				return;
			}
		} catch {
			// Treated the same as an explicit incompleteness: nothing confirmed
			// the session ended.
		}
		setIncomplete(true);
	}, [logout]);

	useEffect(() => {
		void signOut();
	}, [signOut]);

	if (!incomplete) {
		return (
			<p className="min-h-screen flex items-center justify-center">
				Signing out…
			</p>
		);
	}

	return (
		<div className="min-h-screen flex flex-col items-center justify-center gap-4 px-4 text-center">
			<p className="max-w-prose">
				We could not finish signing you out. Your session may still be usable
				for a few minutes. Please try again.
			</p>
			<Button variant="outline" onClick={() => void signOut()}>
				Try again
			</Button>
		</div>
	);
}
