"use client";

import {
	browserSupportsWebAuthn,
	startRegistration,
} from "@simplewebauthn/browser";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Fingerprint } from "lucide-react";
import { useRef, useState, useSyncExternalStore } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	Banner,
	Button,
	DelayedLoading,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	ErrorState,
	Field,
	Input,
} from "@/shared/ui";
import { mfaMutations } from "../service/mutations";

interface PasskeySetupProps {
	open: boolean;
	onClose: () => void;
}

function registrationError(error: unknown): string {
	if (error instanceof DOMException && error.name === "NotAllowedError") {
		return "Passkey setup was cancelled or timed out. Try again when you're ready.";
	}
	return failureText("set up your passkey", error);
}

function subscribeToBrowserCapability() {
	return () => undefined;
}

function getServerBrowserCapability() {
	return false;
}

export function PasskeySetup({ open, onClose }: PasskeySetupProps) {
	const queryClient = useQueryClient();
	const busy = useRef(false);
	const [nameError, setNameError] = useState<string>();
	const [name, setName] = useState("My passkey");
	const supported = useSyncExternalStore(
		subscribeToBrowserCapability,
		browserSupportsWebAuthn,
		getServerBrowserCapability,
	);

	const registration = useMutation({
		mutationFn: async () => {
			if (!supported) {
				throw new Error("This browser or device does not support passkeys.");
			}
			const begin = await mfaMutations.beginWebAuthnRegistration();
			const credential = await startRegistration({
				optionsJSON: JSON.parse(begin.publicKeyOptionsJson),
			});
			return mfaMutations.finishWebAuthnRegistration(
				begin.ceremonyToken,
				JSON.stringify(credential),
				name.trim() || "Passkey",
			);
		},
		onSuccess: async () => {
			await queryClient.invalidateQueries({ queryKey: ["mfa"] });
			toast.success("Passkey added");
			onClose();
		},
		onSettled: () => {
			busy.current = false;
		},
	});

	return (
		<Dialog
			open={open}
			onOpenChange={(next) =>
				!next && !registration.isPending && !busy.current && onClose()
			}
		>
			<DialogContent className="overflow-hidden p-0 sm:max-w-md">
				<DialogHeader className="px-6 pt-6">
					<DialogTitle>Add a passkey</DialogTitle>
					<DialogDescription>
						Use your device’s screen lock or a security key to sign in.
					</DialogDescription>
				</DialogHeader>

				<div className="space-y-5 px-6 py-6">
					{registration.isError && (
						<ErrorState
							title="Passkey wasn’t added"
							detail={registrationError(registration.error)}
						/>
					)}

					<Field
						label="Passkey name"
						description="Choose a name that helps you recognize this device later."
						error={nameError}
					>
						{(control) => (
							<Input
								{...control}
								value={name}
								onChange={(event) => {
									setName(event.target.value);
									setNameError(undefined);
								}}
								maxLength={100}
								placeholder="Personal laptop"
								disabled={registration.isPending}
							/>
						)}
					</Field>
					<Banner>Your fingerprint or face scan stays on your device.</Banner>

					{!supported && (
						<Banner tone="warning">
							Passkeys are not available in this browser. You can still add an
							authenticator app.
						</Banner>
					)}
				</div>

				<DialogFooter className="border-t px-6 py-4">
					<Button
						variant="outline"
						onClick={onClose}
						disabled={registration.isPending}
					>
						Cancel
					</Button>
					<Button
						onClick={() => {
							if (busy.current || !supported) return;
							if (!name.trim()) {
								setNameError("Enter a passkey name.");
								return;
							}
							busy.current = true;
							registration.mutate();
						}}
						disabled={!supported || registration.isPending}
					>
						<DelayedLoading
							active={registration.isPending}
							label="Waiting for your device"
						/>
						{!registration.isPending && <Fingerprint />}
						{registration.isPending
							? "Waiting for your device…"
							: "Create passkey"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
