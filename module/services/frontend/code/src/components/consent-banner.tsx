"use client";

import { createClient } from "@connectrpc/connect";
import { Cookie, X } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import {
	ConsentPurpose,
	ConsentService,
	type ConsentStatus,
} from "@/gen/saas/accounts/v1/consent_pb";
import { WaitlistService } from "@/gen/saas/accounts/v1/waitlist_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { legalContentConfigured } from "@/lib/legal-config";
import { Button, Switch } from "@/shared/ui";

const STORAGE_KEY = "saas-starter:consent-preferences";
const client = createClient(ConsentService, apiTransport);
const acquisitionClient = createClient(WaitlistService, apiTransport);
const LEGAL_CONTENT_CONFIGURED = legalContentConfigured();

type Choices = { analytics: boolean; marketing: boolean };
type Banner =
	| { kind: "hidden" }
	| { kind: "terms"; status: ConsentStatus }
	| {
			kind: "preferences";
			choices: Choices;
			configure: boolean;
			policyVersion: string;
	  };

function purposeChoice(
	status: ConsentStatus,
	purpose: ConsentPurpose,
): boolean {
	return (
		status.purposes.find((item) => item.purpose === purpose)?.granted ?? false
	);
}

function notifyConsent(choices: Choices, policyVersion: string) {
	window.dispatchEvent(
		new CustomEvent("consentchange", {
			detail: { necessary: true, ...choices, policyVersion },
		}),
	);
}

export function ConsentBanner() {
	const { isAuthenticated, isLoading } = useAuth();
	const [banner, setBanner] = useState<Banner>({ kind: "hidden" });

	useEffect(() => {
		if (isLoading) return;
		let cancelled = false;
		if (isAuthenticated) {
			client
				.getStatus({})
				.then((status) => {
					if (cancelled) return;
					if (status.termsAcceptedVersion !== status.currentTermsVersion) {
						setBanner({ kind: "terms", status });
						return;
					}
					const choices = {
						analytics: purposeChoice(status, ConsentPurpose.ANALYTICS),
						marketing: purposeChoice(status, ConsentPurpose.MARKETING),
					};
					notifyConsent(choices, status.policyVersion);
					if (
						!status.preferencesRecorded ||
						status.preferencesPolicyVersion !== status.policyVersion
					) {
						setBanner({
							kind: "preferences",
							choices,
							configure: false,
							policyVersion: status.policyVersion,
						});
					}
				})
				.catch(() =>
					notifyConsent({ analytics: false, marketing: false }, ""),
				);
			return () => {
				cancelled = true;
			};
		}

		acquisitionClient
			.getAcquisitionStatus({})
			.then((status) => {
				if (cancelled) return;
				const policyVersion = status.consentPolicyVersion;
				const stored = JSON.parse(
					window.localStorage.getItem(STORAGE_KEY) ?? "null",
				) as (Choices & { policyVersion?: string }) | null;
				if (stored?.policyVersion === policyVersion) {
					notifyConsent(
						{
							analytics: Boolean(stored.analytics),
							marketing: Boolean(stored.marketing),
						},
						policyVersion,
					);
				} else {
					setBanner({
						kind: "preferences",
						choices: { analytics: false, marketing: false },
						configure: false,
						policyVersion,
					});
				}
			})
			.catch(() =>
				notifyConsent({ analytics: false, marketing: false }, ""),
			);
		return () => {
			cancelled = true;
		};
	}, [isAuthenticated, isLoading]);

	async function savePreferences(choices: Choices, policyVersion: string) {
		if (isAuthenticated) {
			await client.updatePreferences({
				policyVersion,
				...choices,
				region: Intl.DateTimeFormat().resolvedOptions().timeZone,
				context: "consent_banner",
			});
		} else {
			window.localStorage.setItem(
				STORAGE_KEY,
				JSON.stringify({ policyVersion, ...choices }),
			);
		}
		notifyConsent(choices, policyVersion);
		setBanner({ kind: "hidden" });
	}

	if (banner.kind === "hidden") return null;

	return (
		<div
			role="dialog"
			aria-label={
				banner.kind === "terms"
					? "Terms acceptance"
					: "Optional tracking preferences"
			}
			className="fixed bottom-4 left-4 right-4 z-50 mr-auto max-w-lg rounded-2xl border bg-card p-5 shadow-2xl"
		>
			<div className="flex items-start gap-3">
				<div className="rounded-lg bg-primary/10 p-2">
					<Cookie className="h-4 w-4 text-primary" />
				</div>
				<div className="min-w-0 flex-1">
					<h2 className="font-semibold">
						{banner.kind === "terms"
							? "Review the current Terms"
							: "Your privacy choices"}
					</h2>
					{banner.kind === "terms" ? (
						<>
							<p className="mt-1 text-sm text-muted-foreground">
								Terms acceptance is required for your account and is separate
								from optional analytics or marketing choices. Review our{" "}
								<Link className="underline" href="/legal/terms">
									Terms
								</Link>{" "}
								and{" "}
								<Link className="underline" href="/legal/privacy">
									Privacy Policy
								</Link>
								.
							</p>
							{!LEGAL_CONTENT_CONFIGURED && (
								<p className="mt-2 text-sm text-destructive">
									Terms acceptance is unavailable until the operator configures
									its legal content.
								</p>
							)}
						</>
					) : (
						<>
							<p className="mt-1 text-sm text-muted-foreground">
								Necessary storage keeps the service working and cannot be
								disabled. Optional SDKs stay off until you permit their purpose.
							</p>
							{banner.configure && (
								<div className="mt-4 space-y-3">
									<div className="flex items-center justify-between gap-4 text-sm">
										<span>
											<span className="font-medium">Analytics</span>
											<span className="block text-xs text-muted-foreground">
												Product usage measurement
											</span>
										</span>
										<Switch
											aria-label="Allow analytics"
											checked={banner.choices.analytics}
											onCheckedChange={(checked) =>
												setBanner({
													...banner,
													choices: {
														...banner.choices,
														analytics: checked,
													},
												})
											}
										/>
									</div>
									<div className="flex items-center justify-between gap-4 text-sm">
										<span>
											<span className="font-medium">Marketing</span>
											<span className="block text-xs text-muted-foreground">
												Optional product communications
											</span>
										</span>
										<Switch
											aria-label="Allow marketing"
											checked={banner.choices.marketing}
											onCheckedChange={(checked) =>
												setBanner({
													...banner,
													choices: {
														...banner.choices,
														marketing: checked,
													},
												})
											}
										/>
									</div>
								</div>
							)}
						</>
					)}
				</div>
				{banner.kind === "preferences" && (
					<button
						type="button"
						aria-label="Decide later"
						onClick={() => setBanner({ kind: "hidden" })}
						className="p-1 text-muted-foreground"
					>
						<X className="h-4 w-4" />
					</button>
				)}
			</div>
			<div className="mt-4 flex flex-wrap justify-end gap-2">
				{banner.kind === "terms" ? (
					<Button
						disabled={!LEGAL_CONTENT_CONFIGURED}
						onClick={async () => {
							const status = await client.acceptTerms({
								version: banner.status.currentTermsVersion,
								context: "consent_banner",
							});
							const choices = {
								analytics: purposeChoice(status, ConsentPurpose.ANALYTICS),
								marketing: purposeChoice(status, ConsentPurpose.MARKETING),
							};
							setBanner({
								kind: "preferences",
								choices,
								configure: false,
								policyVersion: status.policyVersion,
							});
						}}
					>
						Accept Terms
					</Button>
				) : banner.configure ? (
					<Button
						onClick={() =>
							savePreferences(banner.choices, banner.policyVersion)
						}
					>
						Save choices
					</Button>
				) : (
					<>
						<Button
							variant="outline"
							onClick={() =>
								savePreferences(
									{ analytics: false, marketing: false },
									banner.policyVersion,
								)
							}
						>
							Reject optional
						</Button>
						<Button
							variant="outline"
							onClick={() => setBanner({ ...banner, configure: true })}
						>
							Configure
						</Button>
						<Button
							onClick={() =>
								savePreferences(
									{ analytics: true, marketing: true },
									banner.policyVersion,
								)
							}
						>
							Accept all
						</Button>
					</>
				)}
			</div>
		</div>
	);
}
