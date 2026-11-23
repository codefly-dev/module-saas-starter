"use client";

import { useCallback, useEffect, useRef, useState } from "react";
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
// The sign-out WAITS for the session to be read. Opened by address, this page
// mounts while the provider is still exchanging the refresh cookie for the
// person's session. A sign-out started then carried no bearer, so the host was
// never asked to end the session (SessionService.End) and the server revoked
// the refresh family alone: the session's access tokens, and the role views and
// casts opened from it, stayed live until they expired, behind a page that
// reported a finished sign-out. It also raced that exchange for the one
// refresh cookie, which the loser presented after it was revoked.
export function LogoutPage() {
	const { logout, isLoading } = useAuth();
	const [incomplete, setIncomplete] = useState(false);
	// One sign-out per ask. `logout` changes identity as the session it reads
	// changes — the sign-out itself clears it — and an effect runs again for
	// that; a second run joins the sign-out already made instead of making another.
	const attempt = useRef<Promise<boolean> | null>(null);

	// The outcome arrives through a CALLBACK, which is the shape the react-hooks
	// rule asks for: an effect may start external work and set state when that
	// work reports back, but may not set state in its own body — synchronously or
	// through a helper it awaits, which the compiler traces either way.
	const signOut = useCallback(
		(report: (incomplete: boolean) => void) => {
			attempt.current ??= logout().then(
				({ complete }) => {
					if (complete) window.location.replace("/auth/login");
					return complete;
				},
				// A rejection is the same fact as an explicit incompleteness:
				// nothing confirmed the session ended.
				() => false,
			);
			void attempt.current.then((complete) => {
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
					attempt.current = null;
					setIncomplete(false);
					signOut(setIncomplete);
				}}
			>
				Try again
			</Button>
		</Page>
	);
}
