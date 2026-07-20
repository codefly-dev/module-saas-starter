"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fingerprint, ShieldCheck, Smartphone } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/ui";
import type { MFADevice } from "../model/types";
import { mfaMutations } from "../service/mutations";
import { mfaQueries } from "../service/queries";
import { MFADevices } from "./mfa-devices";
import { MFASetup } from "./mfa-setup";
import { PasskeySetup } from "./passkey-setup";

export function MFASettingsPage() {
	const queryClient = useQueryClient();
	const [showSetup, setShowSetup] = useState(false);
	const [showPasskeySetup, setShowPasskeySetup] = useState(false);

	const { data, isLoading } = useQuery(mfaQueries.devices());
	const devices: MFADevice[] =
		(data as { devices?: MFADevice[] } | undefined)?.devices ?? [];

	const revokeMutation = useMutation({
		mutationFn: (deviceId: string) => mfaMutations.revokeDevice(deviceId),
		onSuccess: () => {
			toast.success("Device revoked");
			queryClient.invalidateQueries({ queryKey: ["mfa"] });
		},
		onError: () => toast.error("Failed to revoke device"),
	});

	const handleRevoke = useCallback(
		(device: MFADevice) => revokeMutation.mutate(device.id),
		[revokeMutation],
	);

	return (
		<div className="space-y-6">
			<div>
				<h2 className="text-2xl font-bold tracking-tight">
					Multi-factor authentication
				</h2>
				<p className="text-muted-foreground">
					Protect your account with passkeys, security keys, or one-time codes.
				</p>
			</div>

			<Card>
				<CardHeader className="gap-5 sm:flex sm:flex-row sm:items-center sm:justify-between">
					<div className="flex items-center gap-3">
						<ShieldCheck className="h-5 w-5 text-primary" />
						<div>
							<CardTitle className="text-lg">Security methods</CardTitle>
							<CardDescription>
								{devices.length > 0
									? `${devices.length} device${devices.length !== 1 ? "s" : ""} enrolled`
									: "No methods enrolled. Add one to secure your account."}
							</CardDescription>
						</div>
					</div>
					<div className="flex flex-wrap gap-2">
						<Button onClick={() => setShowPasskeySetup(true)}>
							<Fingerprint className="mr-2 h-4 w-4" />
							Add passkey
						</Button>
						<Button variant="outline" onClick={() => setShowSetup(true)}>
							<Smartphone className="mr-2 h-4 w-4" />
							Authenticator app
						</Button>
					</div>
				</CardHeader>
				<CardContent>
					<MFADevices
						data={devices}
						isLoading={isLoading}
						onRevoke={handleRevoke}
					/>
				</CardContent>
			</Card>

			{showSetup && <MFASetup open onClose={() => setShowSetup(false)} />}
			{showPasskeySetup && (
				<PasskeySetup open onClose={() => setShowPasskeySetup(false)} />
			)}
		</div>
	);
}
