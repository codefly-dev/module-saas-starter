"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fingerprint, Smartphone } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	ErrorState,
	Page as PageBody,
	PageHeader,
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

	const { data, isLoading, isError, error } = useQuery(mfaQueries.devices());
	const devices: MFADevice[] =
		(data as { devices?: MFADevice[] } | undefined)?.devices ?? [];

	const revokeMutation = useMutation({
		mutationFn: (deviceId: string) => mfaMutations.revokeDevice(deviceId),
		onSuccess: async () => {
			toast.success("Device removed");
			await queryClient.invalidateQueries({ queryKey: ["mfa"] });
		},
	});

	const { mutateAsync: revoke } = revokeMutation;
	const handleRevoke = useCallback(
		(device: MFADevice) => revoke(device.id),
		[revoke],
	);

	return (
		<PageBody>
			<PageHeader
				title="Multi-factor authentication"
				description="Protect your account with passkeys, security keys, or one-time codes."
				actions={
					<>
						<Button variant="outline" onClick={() => setShowSetup(true)}>
							<Smartphone />
							Authenticator app
						</Button>
						<Button onClick={() => setShowPasskeySetup(true)}>
							<Fingerprint />
							Add passkey
						</Button>
					</>
				}
			/>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2}>
						Security methods
					</CardTitle>
					<CardDescription>
						{isError
							? "Your security methods could not be loaded."
							: isLoading
								? "Loading your security methods…"
								: devices.length > 0
									? `${devices.length} device${devices.length !== 1 ? "s" : ""} enrolled`
									: "No methods enrolled. Add one to secure your account."}
					</CardDescription>
				</CardHeader>
				<CardContent>
					{/* A failed read is not "no methods enrolled": whether the account
					    is protected is unknown, and the page says so. */}
					{isError ? (
						<ErrorState
							title="Couldn't load your security methods"
							detail={failureText("load your security methods", error)}
						/>
					) : (
						<MFADevices
							data={devices}
							isLoading={isLoading}
							onRevoke={handleRevoke}
						/>
					)}
				</CardContent>
			</Card>

			{showSetup && <MFASetup open onClose={() => setShowSetup(false)} />}
			{showPasskeySetup && (
				<PasskeySetup open onClose={() => setShowPasskeySetup(false)} />
			)}
		</PageBody>
	);
}
