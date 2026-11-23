"use client";

import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { Cookie } from "lucide-react";
import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { consentQueries } from "@/features/consent/service/queries";
import {
	ConsentPurpose,
	ConsentService,
	type ConsentStatus,
} from "@/gen/saas/accounts/v1/consent_pb";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { signOutAndLeave } from "@/lib/sign-out-journey";
import { Button, Notice, Switch } from "@/shared/ui";

const STORAGE_KEY = "saas-starter:consent-preferences";
const client = createClient(ConsentService, apiTransport);

type Choices = { analytics: boolean; marketing: boolean };
const NOTHING_OPTIONAL: Choices = { analytics: false, marketing: false };
/** What a read of the person's consent says: the notice to draw, and the choices to tell the page of, if it states any. */
type Asked = {
	banner: Banner;
	consent?: { choices: Choices; policyVersion: string };
};
const NOT_ASKED: Asked = { banner: { kind: "hidden" } };
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

// The terms gate requires configured legal content. The root layout resolves it
// on the server at request time (dev placeholders included when the build opts
// in) and passes only whether it is complete.
export function ConsentBanner({
	legalConfigured,
}: {
	legalConfigured: boolean;
}) {
	const { isAuthenticated, isLoading, logout } = useAuth();
	const termsEnabled = legalConfigured;
	// Both reads go through the shared query layer: one request for everything
	// on the page that reads the same thing, and one where an effect of this
	// component's own made two as it mounted in a development build.
	const signedIn = useQuery({
		...consentQueries.status(),
		enabled: !isLoading && isAuthenticated,
	});
	const visitor = useQuery({
		...consentQueries.acquisition(),
		enabled: !isLoading && !isAuthenticated,
	});
	const status = isAuthenticated ? signedIn.data : undefined;
	const failed = isAuthenticated ? signedIn.isError : visitor.isError;
	const policy = isAuthenticated ? undefined : visitor.data;

	const asked = useMemo<Asked>(() => {
		if (isLoading) return NOT_ASKED;
		// A read that failed permits nothing optional, and asks nothing.
		if (failed)
			return {
				banner: { kind: "hidden" },
				consent: { choices: NOTHING_OPTIONAL, policyVersion: "" },
			};
		if (status) {
			if (status.termsAcceptedVersion !== status.currentTermsVersion)
				return { banner: { kind: "terms", status } };
			const choices = {
				analytics: purposeChoice(status, ConsentPurpose.ANALYTICS),
				marketing: purposeChoice(status, ConsentPurpose.MARKETING),
			};
			const consent = { choices, policyVersion: status.policyVersion };
			if (
				!status.preferencesRecorded ||
				status.preferencesPolicyVersion !== status.policyVersion
			)
				return {
					banner: {
						kind: "preferences",
						choices,
						configure: false,
						policyVersion: status.policyVersion,
					},
					consent,
				};
			return { banner: { kind: "hidden" }, consent };
		}
		if (policy) {
			const policyVersion = policy.consentPolicyVersion;
			const stored = JSON.parse(
				window.localStorage.getItem(STORAGE_KEY) ?? "null",
			) as (Choices & { policyVersion?: string }) | null;
			if (stored?.policyVersion === policyVersion)
				return {
					banner: { kind: "hidden" },
					consent: {
						choices: {
							analytics: Boolean(stored.analytics),
							marketing: Boolean(stored.marketing),
						},
						policyVersion,
					},
				};
			return {
				banner: {
					kind: "preferences",
					choices: NOTHING_OPTIONAL,
					configure: false,
					policyVersion,
				},
			};
		}
		return NOT_ASKED;
	}, [isLoading, failed, status, policy]);

	// The page is told what the read states, once per read.
	useEffect(() => {
		if (asked.consent)
			notifyConsent(asked.consent.choices, asked.consent.policyVersion);
	}, [asked]);

	// What the person has done with the notice since the read that drew it.
	const [answered, setBanner] = useState<{ to: Asked; banner: Banner } | null>(
		null,
	);
	const banner = answered?.to === asked ? answered.banner : asked.banner;
	const show = (next: Banner) => setBanner({ to: asked, banner: next });

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
		show({ kind: "hidden" });
	}

	if (banner.kind === "hidden") return null;

	if (banner.kind === "terms") {
		const status = banner.status;
		return (
			<Notice
				title="Review the current Terms"
				icon={<Cookie />}
				// The notice floats over the account menu, so it carries its own way
				// out: a user who cannot, or will not, accept must still be able to
				// leave the session. The kit renders it always, never disabled.
				escape={{
					label: "Sign out",
					// The shared journey, not a redirect of its own: an incomplete
					// sign-out must not land on the login page, which would present a
					// session the server could not revoke as ended.
					onSelect: () => signOutAndLeave(logout),
				}}
				actions={
					<Button
						disabled={!termsEnabled}
						onClick={async () => {
							const accepted = await client.acceptTerms({
								version: status.currentTermsVersion,
								context: "consent_banner",
							});
							show({
								kind: "preferences",
								choices: {
									analytics: purposeChoice(accepted, ConsentPurpose.ANALYTICS),
									marketing: purposeChoice(accepted, ConsentPurpose.MARKETING),
								},
								configure: false,
								policyVersion: accepted.policyVersion,
							});
						}}
					>
						Accept Terms
					</Button>
				}
			>
				<p>
					Terms acceptance is required for your account and is separate from
					optional analytics or marketing choices. Review our{" "}
					<Link className="underline" href="/legal/terms">
						Terms
					</Link>{" "}
					and{" "}
					<Link className="underline" href="/legal/privacy">
						Privacy Policy
					</Link>
					.
				</p>
				{!termsEnabled && (
					<p className="mt-2 text-destructive">
						Terms acceptance is unavailable until the operator configures its
						legal content.
					</p>
				)}
			</Notice>
		);
	}

	return (
		<Notice
			title="Your privacy choices"
			icon={<Cookie />}
			escape={{
				label: "Decide later",
				presentation: "dismiss",
				onSelect: () => show({ kind: "hidden" }),
			}}
			actions={
				banner.configure ? (
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
							onClick={() => show({ ...banner, configure: true })}
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
				)
			}
		>
			<p>
				Necessary storage keeps the service working and cannot be disabled.
				Optional SDKs stay off until you permit their purpose.
			</p>
			{banner.configure && (
				<div className="mt-4 space-y-3 text-foreground">
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
								show({
									...banner,
									choices: { ...banner.choices, analytics: checked },
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
								show({
									...banner,
									choices: { ...banner.choices, marketing: checked },
								})
							}
						/>
					</div>
				</div>
			)}
		</Notice>
	);
}
