"use client";

import { CopyButton } from "@codefly-dev/ui/content";
import { Code, ConnectError } from "@connectrpc/connect";
import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
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
	QRCode,
	Spinner,
	useLoadingPhase,
} from "@/shared/ui";
import { type VerifyTOTPValues, verifyTOTPSchema } from "../model/schemas";
import { mfaMutations } from "../service/mutations";

interface MFASetupProps {
	open: boolean;
	onClose: () => void;
}

type SetupStep = "qr" | "verify" | "backup";

export function MFASetup({ open, onClose }: MFASetupProps) {
	const queryClient = useQueryClient();
	const [step, setStep] = useState<SetupStep>("qr");
	const [provisioningUri, setProvisioningUri] = useState<string | null>(null);
	const [secret, setSecret] = useState<string | null>(null);
	const [backupCodes, setBackupCodes] = useState<string[]>([]);
	const verifying = useRef(false);
	// SetupTOTP is not idempotent: each call writes another unverified device
	// row with another secret. So the session effect below runs its body once per
	// opening, and this is the latch that holds it to once.
	const requested = useRef(false);

	const form = useForm<VerifyTOTPValues>({
		resolver: zodResolver(verifyTOTPSchema),
		defaultValues: { code: "" },
	});

	const setupMutation = useMutation({
		mutationFn: () => mfaMutations.setupTOTP(),
		onSuccess: (data) => {
			setProvisioningUri(data.provisioningUri);
			setSecret(data.secret);
		},
		// No toast: the QR step renders the failure where the code would have
		// been, with the retry beside it. A toast on top of that is the same news
		// twice, and it is the copy that disappears before the reader acts on it.
	});
	const startSetup = setupMutation.mutate;
	const resetSetup = setupMutation.reset;

	const backupMutation = useMutation({
		mutationFn: () => mfaMutations.generateBackupCodes(),
		onSuccess: (result) => setBackupCodes(result.backupCodes),
	});
	const verifyMutation = useMutation({
		mutationFn: (code: string) => mfaMutations.verifyTOTP(code),
		onSuccess: () => {
			toast.success("Authenticator app added");
			setStep("backup");
			backupMutation.mutate();
			queryClient.invalidateQueries({ queryKey: ["mfa"] });
		},
		onSettled: () => {
			verifying.current = false;
		},
	});
	const resetVerification = verifyMutation.reset;
	const resetBackup = backupMutation.reset;
	const { quiet, indicator } = useLoadingPhase(setupMutation.isPending);
	const pending =
		setupMutation.isPending ||
		verifyMutation.isPending ||
		backupMutation.isPending;
	const handleOpen = (isOpen: boolean) => {
		if (!isOpen && !pending && !verifying.current) onClose();
	};

	// `open` becoming true IS the start of a session, and this is the only place
	// that decides what a session starts with.
	//
	// The resets used to live in `handleOpen`, which Base UI calls only for a
	// close the reader performed — so a parent that honours the `open` prop by
	// flipping it never reached them, and reopening showed the previous session's
	// QR code, or its one-time backup codes, with no new request made. Resetting
	// on close instead of on open would also wipe the dialog's content out from
	// under its closing animation.
	//
	// The request itself used to be made *during render*, guarded by
	// `!provisioningUri && !setupMutation.isPending`. That guard holds while a
	// request is in flight and then stops holding: a failure left both of them
	// false, so the next render asked again, and the re-render that answer caused
	// asked again. Measured against a failing endpoint, that was 11,200 SetupTOTP
	// calls in under a second — 11,200 unverified device rows — under a dialog
	// that showed the reader nothing but a wait. An effect cannot spin like that,
	// and the failure below now has a retry the reader presses instead.
	useEffect(() => {
		if (!open) {
			requested.current = false;
			return;
		}
		if (requested.current) return;
		requested.current = true;
		setStep("qr");
		setProvisioningUri(null);
		setSecret(null);
		setBackupCodes([]);
		form.reset();
		resetVerification();
		resetBackup();
		resetSetup();
		startSetup();
	}, [open, startSetup, resetSetup, resetVerification, resetBackup, form]);

	return (
		<Dialog open={open} onOpenChange={handleOpen}>
			<DialogContent className="sm:max-w-[425px]">
				{step === "qr" && (
					<>
						<DialogHeader>
							<DialogTitle>Set up an authenticator app</DialogTitle>
							<DialogDescription>
								Scan the QR code with your authenticator app.
							</DialogDescription>
						</DialogHeader>
						<div className="space-y-4">
							{/* One box of the same height for all three states, so the
							    dialog does not resize under the reader when the code
							    arrives or the request fails. Absolute, like the plate it
							    reserves room for — `min-h-56` is a `--spacing` multiple, so
							    a density skin would move the reservation and leave the jump
							    it exists to prevent. */}
							<div className="flex min-h-[14rem] items-center justify-center">
								{indicator ? (
									<Spinner label="Preparing your authenticator code" />
								) : quiet ? null : provisioningUri ? (
									<QRCode
										value={provisioningUri}
										label="Scan this code with your authenticator app to add this account. The same secret is written out below the code."
										size="lg"
									/>
								) : setupMutation.isError ? (
									<div className="space-y-3">
										<ErrorState
											title="We could not prepare your authenticator code"
											detail={failureText(
												"prepare your authenticator code",
												setupMutation.error,
											)}
										/>
										{ConnectError.from(setupMutation.error).code !==
											Code.PermissionDenied && (
											<Button
												variant="outline"
												size="sm"
												onClick={() => startSetup()}
												disabled={setupMutation.isPending}
											>
												Try again
											</Button>
										)}
									</div>
								) : (
									<DelayedLoading
										active
										label="Preparing your authenticator code"
									/>
								)}
							</div>
							{secret && (
								<div className="space-y-1">
									<p className="type-label">Or enter this code manually:</p>
									<div className="flex items-center gap-2">
										<code className="flex-1 rounded bg-muted px-3 py-2 font-mono text-sm break-all tracking-widest">
											{secret}
										</code>
										<CopyButton text={secret} label="setup code" />
									</div>
								</div>
							)}
						</div>
						<DialogFooter>
							<Button variant="outline" disabled={pending} onClick={onClose}>
								Cancel
							</Button>
							<Button onClick={() => setStep("verify")} disabled={!secret}>
								Next
							</Button>
						</DialogFooter>
					</>
				)}

				{step === "verify" && (
					<>
						<DialogHeader>
							<DialogTitle>Verify your code</DialogTitle>
							<DialogDescription>
								Enter the 6-digit code from your authenticator app to confirm
								setup.
							</DialogDescription>
						</DialogHeader>
						<form
							noValidate
							onSubmit={form.handleSubmit((vals) => {
								if (verifying.current || pending) return;
								verifying.current = true;
								verifyMutation.mutate(vals.code);
							})}
							className="space-y-4"
						>
							<Field
								label="Verification code"
								error={form.formState.errors.code?.message}
							>
								{(control) => (
									<Input
										{...control}
										disabled={pending}
										inputMode="numeric"
										autoComplete="one-time-code"
										placeholder="000000"
										maxLength={6}
										className="text-center font-mono text-lg tracking-widest"
										{...form.register("code")}
										autoFocus
									/>
								)}
							</Field>
							{verifyMutation.isError && (
								<ErrorState
									title="Code wasn’t verified"
									detail={failureText(
										"verify your authenticator code",
										verifyMutation.error,
									)}
								/>
							)}

							<DialogFooter>
								<Button
									type="button"
									variant="outline"
									disabled={pending}
									onClick={() => setStep("qr")}
								>
									Back
								</Button>
								<Button type="submit" disabled={verifyMutation.isPending}>
									{verifyMutation.isPending ? "Verifying..." : "Verify"}
								</Button>
							</DialogFooter>
						</form>
					</>
				)}

				{step === "backup" && (
					<>
						<DialogHeader>
							<DialogTitle>Save your backup codes</DialogTitle>
							<DialogDescription>
								Store these codes in a safe place. Each code can be used once if
								you lose access to your authenticator.
							</DialogDescription>
						</DialogHeader>
						<DelayedLoading
							active={backupMutation.isPending}
							label="Preparing backup codes"
						/>
						{backupMutation.isError && (
							<ErrorState
								title="Authenticator app added, but backup codes are unavailable"
								detail={failureText(
									"prepare backup codes",
									backupMutation.error,
								)}
							/>
						)}
						<div className="grid grid-cols-2 gap-2 rounded-lg border bg-muted/50 p-4">
							{backupCodes.map((code) => (
								<code
									key={code}
									className="rounded bg-background px-2 py-1 text-center font-mono text-sm"
								>
									{code}
								</code>
							))}
						</div>
						<DialogFooter>
							{backupCodes.length > 0 && (
								<CopyButton
									text={backupCodes.join("\n")}
									label="all backup codes"
								/>
							)}
							<Button disabled={pending} onClick={onClose}>
								Done
							</Button>
						</DialogFooter>
					</>
				)}
			</DialogContent>
		</Dialog>
	);
}
