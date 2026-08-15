"use client";

import {
	AlertCircle,
	Building2,
	Loader2,
	LogIn,
	ShieldCheck,
	Sparkles,
} from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { BrandMark } from "@/components/brand-mark";
import { useAppearance } from "@/lib/appearance-provider";
import {
	availableProviders,
	isHeaderInjectedProvider,
	useAuth,
} from "@/lib/auth";
import type { FixtureUser } from "@/lib/fixtures/types";
import { publicHandoffDestination } from "@/lib/public-handoff";

interface FixtureResponse {
	name: string;
	users: FixtureUser[];
}

export function LoginPage() {
	const { signInWith, login, loginWithHeaderInjected } = useAuth();
	const { branding } = useAppearance();
	const router = useRouter();
	const searchParams = useSearchParams();
	const destination = useMemo(
		() => publicHandoffDestination(searchParams),
		[searchParams],
	);
	const providers = useMemo(() => availableProviders(), []);
	const headerInjected = useMemo(() => isHeaderInjectedProvider(), []);
	const [error, setError] = useState<string | null>(null);
	const [fixtureUsers, setFixtureUsers] = useState<FixtureUser[]>([]);
	const [loading, setLoading] = useState<string | null>(null);

	// Header-injected mode has no providers to render but is not dev mode; the
	// fixture list must stay hidden.
	const devMode = providers.length === 0 && !headerInjected;

	useEffect(() => {
		if (!devMode) return;
		fetch("/api/fixtures")
			.then((res) => (res.ok ? (res.json() as Promise<FixtureResponse>) : null))
			.then((data) => {
				if (data?.users) setFixtureUsers(data.users);
			})
			.catch(() => {});
	}, [devMode]);

	// Header-injected identity: no button. The gateway already authenticated the
	// user upstream, so exchange the injected header for a session on load.
	useEffect(() => {
		if (!headerInjected) return;
		let cancelled = false;
		loginWithHeaderInjected()
			.then((authenticated) => {
				if (authenticated && !cancelled) router.push(destination);
			})
			.catch((err) => {
				if (!cancelled) {
					setError(err instanceof Error ? err.message : "Login failed");
				}
			});
		return () => {
			cancelled = true;
		};
	}, [headerInjected, loginWithHeaderInjected, router, destination]);

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
			setError(err instanceof Error ? err.message : "Login failed");
			setLoading(null);
		}
	}

	return (
		<div className="min-h-screen grid lg:grid-cols-2 bg-background">
			{/* ──────────────────────────────────────────────────────────
          LEFT — marketing / value-prop panel.
          Hidden on mobile (lg:flex) so the form stays the focus on
          phones; on desktop, this is the difference between "looks
          like a template" and "looks like a product".
          ────────────────────────────────────────────────────────── */}
			<div
				className="hidden lg:flex relative overflow-hidden text-primary-foreground"
				style={{
					background:
						"linear-gradient(135deg, var(--primary), color-mix(in oklab, var(--primary) 72%, var(--foreground)))",
				}}
			>
				{/* Soft radial glow — depth without an image asset. */}
				<div
					aria-hidden
					className="pointer-events-none absolute inset-0 opacity-40"
					style={{
						background:
							"radial-gradient(60rem 30rem at 20% 0%, color-mix(in oklab, currentColor 25%, transparent), transparent 50%), radial-gradient(50rem 25rem at 80% 100%, color-mix(in oklab, currentColor 15%, transparent), transparent 50%)",
					}}
				/>
				{/* Grid pattern overlay — modern Vercel/Linear vibe. */}
				<div
					aria-hidden
					className="pointer-events-none absolute inset-0 opacity-[0.07]"
					style={{
						backgroundImage:
							"linear-gradient(to right, currentColor 1px, transparent 1px), linear-gradient(to bottom, currentColor 1px, transparent 1px)",
						backgroundSize: "32px 32px",
					}}
				/>

				<div className="relative z-10 flex flex-col justify-between p-12 w-full">
					<div className="flex items-center gap-3">
						<BrandMark
							className="flex h-10 w-10 items-center justify-center overflow-hidden rounded-xl bg-primary-foreground/15 text-lg font-bold ring-1 ring-primary-foreground/30 backdrop-blur-sm"
							imageClassName="p-1"
						/>
						<span className="text-xl font-semibold tracking-tight">
							{branding.name}
						</span>
					</div>

					<div className="space-y-8 max-w-md">
						<div>
							<h2 className="text-4xl font-bold tracking-tight leading-tight">
								Welcome back to {branding.name}.
							</h2>
							<p className="mt-4 text-lg text-primary-foreground/80 leading-relaxed">
								Continue to the authenticated product without coupling this
								session to the public company site.
							</p>
						</div>

						<ul className="space-y-3 text-sm">
							<li className="flex items-start gap-3">
								<ShieldCheck className="h-5 w-5 mt-0.5 text-primary-foreground/90 shrink-0" />
								<span className="text-primary-foreground/90">
									<span className="font-medium">Server-validated access</span> —
									authenticated requests are checked again at the service
									boundary.
								</span>
							</li>
							<li className="flex items-start gap-3">
								<Building2 className="h-5 w-5 mt-0.5 text-primary-foreground/90 shrink-0" />
								<span className="text-primary-foreground/90">
									<span className="font-medium">Tenant-aware navigation</span>{" "}
									— organization context and permissions shape the product
									shell.
								</span>
							</li>
							<li className="flex items-start gap-3">
								<Sparkles className="h-5 w-5 mt-0.5 text-primary-foreground/90 shrink-0" />
								<span className="text-primary-foreground/90">
									<span className="font-medium">Independent public site</span> —
									company content and pricing can release on their own cadence.
								</span>
							</li>
						</ul>
					</div>

					<p className="text-xs text-primary-foreground/60">
						Production claims require current evidence for your environment.
					</p>
				</div>
			</div>

			{/* ──────────────────────────────────────────────────────────
          RIGHT — form. Centred on its own column on desktop, full
          width on mobile.
          ────────────────────────────────────────────────────────── */}
			<div className="flex items-center justify-center px-4 py-12">
				<div className="w-full max-w-sm">
					{/* Mobile-only logo (hidden on desktop where the left panel has it) */}
					<div className="flex lg:hidden justify-center mb-8">
						<div className="flex items-center gap-3">
							<BrandMark
								className="flex h-10 w-10 items-center justify-center overflow-hidden rounded-xl bg-primary text-lg font-bold text-primary-foreground shadow-lg shadow-primary/25"
								imageClassName="p-1"
							/>
							<span className="text-xl font-semibold tracking-tight">
								{branding.name}
							</span>
						</div>
					</div>

					<div className="rounded-2xl border bg-card text-card-foreground shadow-xl shadow-black/5 dark:shadow-black/20">
						<div className="p-8 space-y-6">
							<div className="space-y-1.5">
								<h1 className="text-2xl font-semibold tracking-tight">
									Sign in
								</h1>
								<p className="text-sm text-muted-foreground">
									Choose a method to continue.
								</p>
							</div>

							{error && (
								<div className="flex items-center gap-2 rounded-lg bg-destructive/10 border border-destructive/20 p-3 text-sm text-destructive">
									<AlertCircle className="h-4 w-4 shrink-0" />
									<span>{error}</span>
								</div>
							)}

							{/* Header-injected identity — no button, exchange in progress */}
							{headerInjected && !error && (
								<div className="flex items-center gap-3 rounded-lg border bg-background p-4 text-sm text-muted-foreground">
									<Loader2 className="h-5 w-5 animate-spin shrink-0" />
									<span>Signing you in…</span>
								</div>
							)}

							{/* OAuth providers */}
							{providers.length > 0 && (
								<div className="space-y-2.5">
									{providers.map((p) => (
										<button
											type="button"
											key={p.id}
											onClick={async () => {
												try {
													await signInWith(p.id, destination);
												} catch {}
											}}
											className="w-full flex items-center justify-center gap-2.5 h-11 rounded-lg border bg-background hover:bg-accent/50 text-sm font-medium transition-colors"
										>
											<LogIn className="h-4 w-4" />
											Continue with {p.displayName}
										</button>
									))}
								</div>
							)}

							{/* Fixture users — dev mode */}
							{devMode && fixtureUsers.length > 0 && (
								<div className="space-y-2.5">
									<p className="text-xs font-medium text-muted-foreground text-center uppercase tracking-wider">
										Dev — select a user
									</p>
									{fixtureUsers.map((u) => (
										<button
											type="button"
											key={u.fixture_token ?? u.provider_id}
											onClick={() => handleFixtureLogin(u)}
											disabled={loading === (u.fixture_token ?? u.provider_id)}
											className="w-full flex items-center gap-3 h-14 px-4 rounded-lg border bg-background hover:bg-accent/50 text-left transition-colors disabled:opacity-50"
										>
											{loading === (u.fixture_token ?? u.provider_id) ? (
												<Loader2 className="h-5 w-5 animate-spin text-muted-foreground shrink-0" />
											) : (
												<div className="h-8 w-8 rounded-full bg-primary flex items-center justify-center shrink-0">
													<span className="text-xs font-bold text-white">
														{u.name
															.split(" ")
															.map((n) => n[0])
															.join("")}
													</span>
												</div>
											)}
											<div className="min-w-0">
												<div className="text-sm font-medium truncate">
													{u.name}
												</div>
												<div className="text-xs text-muted-foreground truncate">
													{u.email}
													<span className="ml-1.5 inline-flex items-center rounded-full bg-accent px-1.5 py-0.5 text-[10px] font-medium">
														{u.role}
													</span>
												</div>
											</div>
										</button>
									))}
								</div>
							)}
						</div>
					</div>

					{/* Footer */}
					<p className="text-center text-xs text-muted-foreground mt-6">
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
		</div>
	);
}
