"use client";

import {
	browserSupportsWebAuthn,
	startRegistration,
} from "@simplewebauthn/browser";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Fingerprint, Loader2, ShieldCheck } from "lucide-react";
import { useState, useSyncExternalStore } from "react";
import { toast } from "sonner";

import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Input,
	Label,
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
	return error instanceof Error ? error.message : "Passkey setup failed.";
}

function subscribeToBrowserCapability() {
	return () => undefined;
}

function getServerBrowserCapability() {
	return false;
}

export function PasskeySetup({ open, onClose }: PasskeySetupProps) {
	const queryClient = useQueryClient();
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
		onError: (error) => toast.error(registrationError(error)),
	});

	return (
		<Dialog open={open} onOpenChange={(next) => !next && onClose()}>
			<DialogContent className="overflow-hidden p-0 sm:max-w-md">
				<div className="bg-primary px-6 py-7 text-primary-foreground">
					<div className="mb-4 flex size-11 items-center justify-center rounded-2xl bg-primary-foreground/15 ring-1 ring-primary-foreground/25">
						<Fingerprint className="size-6" />
					</div>
					<DialogHeader>
						<DialogTitle className="text-xl text-primary-foreground">
							Add a passkey
						</DialogTitle>
						<DialogDescription className="text-primary-foreground/75">
							Use Face ID, Touch ID, Windows Hello, or a hardware security key
							for phishing-resistant verification.
						</DialogDescription>
					</DialogHeader>
				</div>

				<div className="space-y-5 px-6 py-6">
					<div className="space-y-2">
						<Label htmlFor="passkey-name">Passkey name</Label>
						<Input
							id="passkey-name"
							value={name}
							onChange={(event) => setName(event.target.value)}
							maxLength={100}
							placeholder="MacBook Touch ID"
							disabled={registration.isPending}
						/>
						<p className="text-xs text-muted-foreground">
							Pick a name that helps you recognize this device later.
						</p>
					</div>

					<div className="flex gap-3 rounded-xl border bg-muted/40 p-4 text-sm">
						<ShieldCheck className="mt-0.5 size-5 shrink-0 text-primary" />
						<p className="leading-5 text-muted-foreground">
							Your biometric never leaves your device. We store only the
							encrypted public credential and authenticator state.
						</p>
					</div>

					{!supported && (
						<p className="rounded-xl border border-destructive/20 bg-destructive/10 p-3 text-sm text-destructive">
							Passkeys are not available in this browser. You can still add an
							authenticator app.
						</p>
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
						onClick={() => registration.mutate()}
						disabled={!supported || registration.isPending || !name.trim()}
					>
						{registration.isPending ? (
							<Loader2 className="animate-spin" />
						) : (
							<Fingerprint />
						)}
						{registration.isPending
							? "Waiting for your device…"
							: "Create passkey"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
