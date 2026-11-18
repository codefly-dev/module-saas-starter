"use client";

import { createClient } from "@connectrpc/connect";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import {
	ConsentPurpose,
	ConsentService,
} from "@/gen/saas/accounts/v1/consent_pb";
import { apiTransport } from "@/lib/connect/transport";
import { failureText } from "@/shared/lib/failure-text";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Skeleton,
	Switch,
	useLoadingPhase,
} from "@/shared/ui";

const client = createClient(ConsentService, apiTransport);

const HEADER = (
	<PageHeader
		title="Privacy choices"
		description="Withdraw optional consent at any time. Save your choices to apply them."
	/>
);

export function ConsentSettingsPage() {
	// The switches mean something only once the person's own choices have
	// arrived: before that, and after a failed read, an "off" switch would be a
	// default passing for an answer, and saving it would withdraw consent the
	// person had given.
	const [load, setLoad] = useState<"loading" | "ready" | "failed">("loading");
	const [analytics, setAnalytics] = useState(false);
	const [marketing, setMarketing] = useState(false);
	const [policyVersion, setPolicyVersion] = useState("");
	const [saving, setSaving] = useState(false);
	const busy = useRef(false);
	const [readError, setReadError] = useState<unknown>();
	const [saveError, setSaveError] = useState<string>();
	const { quiet, indicator } = useLoadingPhase(load === "loading");

	useEffect(() => {
		let cancelled = false;
		client
			.getStatus({})
			.then((status) => {
				if (cancelled) return;
				setPolicyVersion(status.policyVersion);
				setAnalytics(
					status.purposes.find(
						(item) => item.purpose === ConsentPurpose.ANALYTICS,
					)?.granted ?? false,
				);
				setMarketing(
					status.purposes.find(
						(item) => item.purpose === ConsentPurpose.MARKETING,
					)?.granted ?? false,
				);
				setLoad("ready");
			})
			.catch((error) => {
				if (!cancelled) {
					setReadError(error);
					setLoad("failed");
				}
			});
		return () => {
			cancelled = true;
		};
	}, []);

	async function save() {
		if (busy.current || load !== "ready" || !policyVersion) return;
		busy.current = true;
		setSaveError(undefined);
		setSaving(true);
		try {
			await client.updatePreferences({
				policyVersion,
				analytics,
				marketing,
				region: Intl.DateTimeFormat().resolvedOptions().timeZone,
				context: "privacy_settings",
			});
			window.dispatchEvent(
				new CustomEvent("consentchange", {
					detail: { necessary: true, analytics, marketing, policyVersion },
				}),
			);
			toast.success("Privacy choices updated");
		} catch (error) {
			setSaveError(failureText("update your privacy choices", error));
		} finally {
			busy.current = false;
			setSaving(false);
		}
	}

	if (quiet || indicator) {
		return (
			<PageBody>
				{HEADER}
				{indicator && <Skeleton className="h-64 w-full" />}
			</PageBody>
		);
	}

	if (load === "failed") {
		return (
			<PageBody>
				{HEADER}
				<Panel>
					<ErrorState
						title="Couldn't load your privacy choices"
						detail={failureText("load your privacy choices", readError)}
					/>
				</Panel>
			</PageBody>
		);
	}

	return (
		<PageBody>
			{HEADER}
			<Card>
				<CardHeader>
					<CardTitle>Optional uses of your data</CardTitle>
					<CardDescription>Policy version {policyVersion}</CardDescription>
				</CardHeader>
				<CardContent className="space-y-5">
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Necessary</p>
							<p className="text-sm text-muted-foreground">
								Signing you in, keeping your account secure, and doing what you
								ask of the product. Always on.
							</p>
						</div>
						<Switch checked disabled aria-label="Necessary storage enabled" />
					</div>
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Analytics</p>
							<p className="text-sm text-muted-foreground">
								Optional measurement of how the product is used.
							</p>
						</div>
						<Switch
							disabled={saving}
							checked={analytics}
							onCheckedChange={setAnalytics}
							aria-label="Allow analytics"
						/>
					</div>
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Marketing</p>
							<p className="text-sm text-muted-foreground">
								Optional messages about the product.
							</p>
						</div>
						<Switch
							disabled={saving}
							checked={marketing}
							onCheckedChange={setMarketing}
							aria-label="Allow marketing"
						/>
					</div>
					{saveError && (
						<ErrorState
							title="Privacy choices weren’t saved"
							detail={saveError}
						/>
					)}
				</CardContent>
				<CardFooter>
					<Button onClick={save} disabled={saving || !policyVersion}>
						{saving ? "Saving…" : "Save choices"}
					</Button>
				</CardFooter>
			</Card>
		</PageBody>
	);
}
