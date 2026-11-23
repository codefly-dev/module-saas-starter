"use client";

import { useCallback, useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
import { Button, ErrorState, Page, PageHeader, Spinner } from "@/shared/ui";

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
//
// The sign-out presents the refresh cookie and nothing else. Opened by address,
// this page is the one place the provider does not restore the person's session
// first (restoresSessionAtBoot): the server ends the session the cookie names,
// with the role views and casts opened from it, so no bearer is needed, and a
// tab that was never signed in starts none of the shell's signed-in reads for
// the sign-out to refuse. It still waits for the provider to say it is done
// loading, so a page reached with a session already installed signs out as that
// session.
//
// One sign-out per ask, kept outside the component: the shell rebuilds
// everything beneath it when the session's authority changes, this page with
// it, and a sign-out is exactly such a change. A rebuilt page joins the
// sign-out already made instead of making another with nothing to present.
let attempt: Promise<boolean> | null = null;

// Test seam: one test's sign-out is not the next one's.
export function resetSignOutForTests(): void {
	attempt = null;
}

export function LogoutPage() {
	const { logout, isLoading } = useAuth();
	const [incomplete, setIncomplete] = useState(false);

	// The outcome arrives through a CALLBACK, which is the shape the react-hooks
	// rule asks for: an effect may start external work and set state when that
	// work reports back, but may not set state in its own body — synchronously or
	// through a helper it awaits, which the compiler traces either way.
	const signOut = useCallback(
		(report: (incomplete: boolean) => void) => {
			attempt ??= logout().then(
				({ complete }) => {
					if (complete) window.location.replace("/auth/login");
					return complete;
				},
				// A rejection is the same fact as an explicit incompleteness:
				// nothing confirmed the session ended.
				() => false,
			);
			void attempt.then((complete) => {
				if (!complete) report(true);
			});
		},
		[logout],
	);

	useEffect(() => {
		if (isLoading) return;
		let live = true;
		signOut((value) => {
			if (live) {
				setIncomplete(value);
			}
		});
		return () => {
			live = false;
		};
	}, [isLoading, signOut]);

	if (!incomplete) {
		return (
			<Page>
				<PageHeader title="Signing out" />
				<Spinner label="Signing out…" />
			</Page>
		);
	}

	return (
		<Page>
			<PageHeader title="Sign-out incomplete" />
			<ErrorState
				title="We could not finish signing you out"
				detail="Your session may still be usable for a few minutes. Please try again."
			/>
			<Button
				variant="outline"
				onClick={() => {
					attempt = null;
					setIncomplete(false);
					signOut(setIncomplete);
				}}
			>
				Try again
			</Button>
		</Page>
	);
}
