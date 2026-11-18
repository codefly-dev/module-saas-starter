"use client";

import { ArrowLeft, Fingerprint, Loader2 } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { signInFailureText } from "@/features/auth/ui/login-page";
import { useAuth } from "@/lib/auth";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	ErrorState,
	Field,
	Input,
} from "@/shared/ui";

export default function MFAClient() {
	const { completeMFA, completeMFAWithPasskey, cancelMFA } = useAuth();
	const inputRef = useRef<HTMLInputElement>(null);
	const [code, setCode] = useState("");
	const [error, setError] = useState<string | null>(null);
	const [submitting, setSubmitting] = useState(false);
	const [passkeySubmitting, setPasskeySubmitting] = useState(false);

	useEffect(() => inputRef.current?.focus(), []);

	async function submit(event: FormEvent) {
		event.preventDefault();
		const normalized = code.replace(/[\s-]/g, "");
		if (normalized.length < 6) return;
		setError(null);
		setSubmitting(true);
		try {
			await completeMFA(normalized);
		} catch (caught) {
			setError(
				signInFailureText(
					caught,
					"Couldn't check that code. Try again; if it keeps failing, go back and sign in again.",
				),
			);
			setSubmitting(false);
			inputRef.current?.select();
		}
	}

	async function usePasskey() {
		setError(null);
		setPasskeySubmitting(true);
		try {
			await completeMFAWithPasskey();
		} catch (caught) {
			setError(
				signInFailureText(
					caught,
					"Couldn't check your passkey. Try again, or enter a code instead.",
				),
			);
			setPasskeySubmitting(false);
		}
	}

	return (
		<main className="flex min-h-screen w-full items-center justify-center p-4">
			<Card className="w-full max-w-md">
				<CardHeader>
					<h1 data-slot="page-title" className="type-page-title">
						One more check
					</h1>
					<CardDescription>
						Enter the code from your authenticator app, or use one of your
						one-time recovery codes.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<form onSubmit={submit} className="space-y-4">
						<Field
							label="Authentication code"
							description="This sign-in expires after five minutes."
						>
							{(control) => (
								<Input
									{...control}
									ref={inputRef}
									name="code"
									inputMode="numeric"
									autoComplete="one-time-code"
									value={code}
									onChange={(event) => setCode(event.target.value)}
									disabled={submitting}
									placeholder="000 000"
									aria-invalid={Boolean(error)}
									className="text-center font-mono tracking-widest"
								/>
							)}
						</Field>

						{error && <ErrorState title={error} />}

						<div className="space-y-2">
							<Button
								type="submit"
								size="lg"
								className="w-full"
								disabled={submitting || code.replace(/[\s-]/g, "").length < 6}
							>
								{submitting && <Loader2 className="animate-spin" />}
								Verify and continue
							</Button>
							<Button
								type="button"
								variant="outline"
								size="lg"
								className="w-full"
								disabled={submitting || passkeySubmitting}
								onClick={usePasskey}
							>
								{passkeySubmitting ? (
									<Loader2 className="animate-spin" />
								) : (
									<Fingerprint />
								)}
								{passkeySubmitting
									? "Waiting for your passkey…"
									: "Use a passkey"}
							</Button>
							<Button
								type="button"
								variant="ghost"
								className="w-full"
								disabled={submitting || passkeySubmitting}
								onClick={cancelMFA}
							>
								<ArrowLeft />
								Back to sign in
							</Button>
						</div>
					</form>
				</CardContent>
			</Card>
		</main>
	);
}
