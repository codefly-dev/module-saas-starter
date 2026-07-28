"use client";

import { createClient } from "@connectrpc/connect";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import {
	ConsentPurpose,
	ConsentService,
} from "@/gen/saas/accounts/v1/consent_pb";
import { apiTransport } from "@/lib/connect/transport";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	Switch,
} from "@/shared/ui";

const client = createClient(ConsentService, apiTransport);

export function ConsentSettingsPage() {
	const [analytics, setAnalytics] = useState(false);
	const [marketing, setMarketing] = useState(false);
	const [policyVersion, setPolicyVersion] = useState("");
	const [saving, setSaving] = useState(false);

	useEffect(() => {
		client.getStatus({}).then((status) => {
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
		});
	}, []);

	async function save() {
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
		} catch {
			toast.error("Privacy choices could not be updated");
		} finally {
			setSaving(false);
		}
	}

	return (
		<div className="space-y-6">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">Privacy choices</h1>
				<p className="text-muted-foreground">
					Withdraw optional consent at any time. Changes take effect
					immediately.
				</p>
			</div>
			<Card>
				<CardHeader>
					<CardTitle>Purpose-based consent</CardTitle>
					<CardDescription>
						Policy version {policyVersion || "loading"}
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-5">
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Necessary</p>
							<p className="text-sm text-muted-foreground">
								Authentication, security, and requested service operation.
							</p>
						</div>
						<Switch checked disabled aria-label="Necessary storage enabled" />
					</div>
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Analytics</p>
							<p className="text-sm text-muted-foreground">
								Optional product usage measurement.
							</p>
						</div>
						<Switch
							checked={analytics}
							onCheckedChange={setAnalytics}
							aria-label="Allow analytics"
						/>
					</div>
					<div className="flex items-center justify-between gap-4">
						<div>
							<p className="font-medium">Marketing</p>
							<p className="text-sm text-muted-foreground">
								Optional product communications.
							</p>
						</div>
						<Switch
							checked={marketing}
							onCheckedChange={setMarketing}
							aria-label="Allow marketing"
						/>
					</div>
				</CardContent>
				<CardFooter>
					<Button onClick={save} disabled={saving || !policyVersion}>
						{saving ? "Saving…" : "Save choices"}
					</Button>
				</CardFooter>
			</Card>
		</div>
	);
}
