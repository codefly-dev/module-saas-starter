"use client";

import { Loader2, LogIn } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { BrandMark } from "@/components/brand-mark";
import {
	forgetClientAuthorization,
	readClientAuthorizationRequest,
	rememberClientAuthorization,
	validateClientAuthorization,
} from "@/features/auth/model/client-authorization";
import { useAppearance } from "@/lib/appearance-provider";
import {
	availableProviders,
	type IdentityConfig,
	isHeaderInjectedProvider,
	useAuth,
} from "@/lib/auth";
import { AuthError } from "@/lib/auth-errors";
import type { FixtureUser } from "@/lib/fixtures/types";
import { publicHandoffDestination } from "@/lib/public-handoff";
import {
	Avatar,
	AvatarFallback,
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	ErrorState,
} from "@/shared/ui";

const SIGN_IN_FAILED =
	"Couldn't sign you in. Try again; if it keeps failing, tell your administrator.";

interface FixtureResponse {
	name: string;
	users: FixtureUser[];
}

/** Authentication responses can carry server diagnostics; use the action's safe copy. */
export function signInFailureText(error: unknown, fallback: string): string {
	if (
		error instanceof AuthError &&
		(error.detail.status === 403 || error.detail.code === "permission_denied")
	)
		return "Your account does not have access. Ask your organization administrator to grant access.";
	return fallback;
}

export function LoginPage({ identity }: { identity: IdentityConfig }) {
	const { signInWith, login, loginWithHeaderInjected } = useAuth();
	const { branding } = useAppearance();
	const router = useRouter();
	const searchParams = useSearchParams();
	// A sensitive operation the host refused for want of a recent second
	// factor sends the person here with step_up=1 and next=<where they were>.
	const stepUp = searchParams.get("step_up") === "1";
	const destination = useMemo(
		() => publicHandoffDestination(searchParams),
		[searchParams],
	);
	const providers = useMemo(() => availableProviders(identity), [identity]);
	const headerInjected = useMemo(
		() => isHeaderInjectedProvider(identity),
		[identity],
	);
	const [error, setError] = useState<string | null>(null);
	const [fixtureUsers, setFixtureUsers] = useState<FixtureUser[]>([]);
	const [loading, setLoading] = useState<string | null>(null);

	// A registered client sent the person here to sign in on its behalf. The
	// host decides whether that request is allowed before anything else renders:
	// until it does, no sign-in method is offered, and a refusal offers none at
	// all. `pending` is that gap, and it is why the checks below read three ways
	// rather than two.
	const clientRequest = useMemo(
		() => readClientAuthorizationRequest(searchParams),
		[searchParams],
	);
	const [clientName, setClientName] = useState<string | null>(null);
	const [clientRefusal, setClientRefusal] = useState<string | null>(null);
	const clientPending =
		clientRequest !== null && clientName === null && clientRefusal === null;

	useEffect(() => {
		if (!clientRequest) {
			forgetClientAuthorization();
			return;
		}
		let cancelled = false;
		validateClientAuthorization(clientRequest)
			.then((resolution) => {
				if (cancelled) return;
				// The host's answer is remembered alongside the request, because
				// the consent page must render what the HOST said about this
				// client — its validated name and origin, the resource — and never
				// what the query string claimed.
				rememberClientAuthorization(clientRequest, resolution);
				setClientName(resolution.clientName || clientRequest.clientId);
			})
			.catch((err) => {
				if (cancelled) return;
				forgetClientAuthorization();
				setClientRefusal(
					signInFailureText(
						err,
						"Couldn't check the application that sent you here. Go back to it and start again.",
					),
				);
			});
		return () => {
			cancelled = true;
		};
	}, [clientRequest]);

	// Header-injected mode has no providers to render but is not dev mode; the
	// fixture list must stay hidden.
	const devMode = providers.length === 0 && !headerInjected;

	useEffect(() => {
		if (!devMode || clientRefusal) return;
		fetch("/api/fixtures")
			.then((res) => (res.ok ? (res.json() as Promise<FixtureResponse>) : null))
			.then((data) => {
				if (data?.users) setFixtureUsers(data.users);
			})
			.catch(() => {});
	}, [devMode, clientRefusal]);

	// Header-injected identity: no button. The gateway already authenticated the
	// user upstream, so exchange the injected header for a session on load.
	useEffect(() => {
		if (!headerInjected || clientPending || clientRefusal) return;
		let cancelled = false;
		loginWithHeaderInjected()
			.then((authenticated) => {
				if (authenticated && !cancelled) router.push(destination);
			})
			.catch((err) => {
				if (!cancelled) {
					setError(signInFailureText(err, SIGN_IN_FAILED));
				}
			});
		return () => {
			cancelled = true;
		};
	}, [
		headerInjected,
		loginWithHeaderInjected,
		router,
		destination,
		clientPending,
		clientRefusal,
	]);

	async function handleFixtureLogin(user: FixtureUser) {
		setError(null);
		const fixtureToken = user.fixture_token ?? user.provider_id;
		setLoading(fixtureToken);
		try {
			const authenticated = await login(
				user.provider,
				fixtureToken,
				user.email,
			);
			// Client navigation (not a full reload): preserves the in-memory authed
			// state login() just set in the root AuthProvider, so the dashboard
			// renders immediately. A full window.location reload would drop that
			// state and force a cross-origin refresh round-trip (fragile when the
			// FE and api are on different ports, e.g. the e2e direct-to-api setup).
			if (authenticated) router.push(destination);
		} catch (err) {
			setError(signInFailureText(err, SIGN_IN_FAILED));
			setLoading(null);
		}
	}

	return (
		<div className="flex min-h-screen w-full items-center justify-center p-4">
			<div className="w-full max-w-md space-y-6">
				<div className="flex items-center justify-center gap-3">
					<BrandMark
						className="flex size-10 items-center justify-center overflow-hidden rounded-xl bg-primary text-lg font-bold text-primary-foreground"
						imageClassName="p-1"
					/>
					<span className="text-xl font-semibold tracking-tight">
						{branding.name}
					</span>
				</div>

				<Card>
					<CardHeader>
						<h1 data-slot="page-title" className="type-page-title">
							{stepUp ? "Confirm your passkey" : "Sign in"}
						</h1>
						<CardDescription>
							{stepUp
								? "That change needs your second factor confirmed within the last 15 minutes. Sign in again and you will be taken back to where you were."
								: clientName
									? `to continue to ${clientName}`
									: "Choose a method to continue."}
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						{/* A client the host does not recognise is refused here, before
						    any sign-in method is offered. */}
						{clientRefusal && <ErrorState title={clientRefusal} />}

						{clientPending && (
							<p
								role="status"
								className="flex items-center gap-2 text-sm text-muted-foreground"
							>
								<Loader2 className="size-4 shrink-0 animate-spin" />
								Checking the application…
							</p>
						)}

						{error && <ErrorState title={error} />}

						{/* Header-injected identity — no button, exchange in progress */}
						{headerInjected && !error && !clientPending && !clientRefusal && (
							<p
								role="status"
								className="flex items-center gap-2 text-sm text-muted-foreground"
							>
								<Loader2 className="size-4 shrink-0 animate-spin" />
								Signing you in…
							</p>
						)}

						{/* OAuth providers */}
						{providers.length > 0 && !clientPending && !clientRefusal && (
							<div className="space-y-2">
								{providers.map((p) => (
									<Button
										key={p.id}
										variant="outline"
										size="lg"
										className="w-full"
										onClick={async () => {
											setError(null);
											try {
												await signInWith(p, destination);
											} catch (err) {
												// A failed `begin` leaves the button on-screen with no
												// redirect, so the click otherwise looks dead. Log the
												// status for operators; show the user a real message.
												console.error("Sign-in could not be started", err);
												setError(
													"Sign-in is temporarily misconfigured. Please try again in a few minutes.",
												);
											}
										}}
									>
										<LogIn />
										Continue with {p.displayName}
									</Button>
								))}
							</div>
						)}

						{/* Fixture users — dev mode */}
						{devMode &&
							fixtureUsers.length > 0 &&
							!clientPending &&
							!clientRefusal && (
								<div className="space-y-2">
									<p className="text-xs font-medium text-muted-foreground">
										Development sign-in — choose a person
									</p>
									{fixtureUsers.map((u) => {
										const token = u.fixture_token ?? u.provider_id;
										return (
											<Button
												key={token}
												variant="outline"
												className="h-auto w-full justify-start gap-3 px-3 py-2 text-left"
												onClick={() => handleFixtureLogin(u)}
												disabled={loading === token}
											>
												{loading === token ? (
													<Loader2 className="size-8 shrink-0 animate-spin p-1.5 text-muted-foreground" />
												) : (
													<Avatar>
														<AvatarFallback>
															{u.name
																.split(" ")
																.map((n) => n[0])
																.join("")}
														</AvatarFallback>
													</Avatar>
												)}
												<span className="min-w-0 flex-1">
													<span className="block truncate text-sm font-medium">
														{u.name}
													</span>
													<span className="block truncate text-xs font-normal text-muted-foreground">
														{u.email}
													</span>
												</span>
												<Badge variant="secondary">{u.role}</Badge>
											</Button>
										);
									})}
								</div>
							)}
					</CardContent>
				</Card>

				<p className="text-center text-xs text-muted-foreground">
					Continuing starts authentication. You will review and accept the
					current{" "}
					<a href="/legal/terms" className="underline">
						Terms of Service
					</a>{" "}
					separately. See our{" "}
					<a href="/legal/privacy" className="underline">
						Privacy Policy
					</a>
					.
				</p>
			</div>
		</div>
	);
}
