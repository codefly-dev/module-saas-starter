"use client";

import {
	AlertCircle,
	ArrowLeft,
	Fingerprint,
	KeyRound,
	Loader2,
	ShieldCheck,
} from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/lib/auth";

export default function MFAChallengePage() {
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
				caught instanceof Error
					? caught.message
					: "That code was not accepted.",
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
				caught instanceof Error
					? caught.message
					: "That passkey was not accepted.",
			);
			setPasskeySubmitting(false);
		}
	}

	return (
		<main className="relative isolate flex min-h-screen w-full items-center justify-center overflow-hidden px-4 py-12">
			<div
				aria-hidden
				className="absolute inset-0 -z-20 bg-[radial-gradient(circle_at_top_left,color-mix(in_oklab,var(--primary)_18%,transparent),transparent_42%),radial-gradient(circle_at_bottom_right,color-mix(in_oklab,var(--primary)_12%,transparent),transparent_38%)]"
			/>
			<div
				aria-hidden
				className="absolute inset-0 -z-10 opacity-[0.035] [background-image:linear-gradient(to_right,currentColor_1px,transparent_1px),linear-gradient(to_bottom,currentColor_1px,transparent_1px)] [background-size:36px_36px]"
			/>

			<section className="w-full max-w-md overflow-hidden rounded-3xl border bg-card/90 shadow-2xl shadow-primary/10 backdrop-blur-xl">
				<div className="border-b bg-primary px-8 py-9 text-primary-foreground">
					<div className="mb-6 flex size-12 items-center justify-center rounded-2xl bg-primary-foreground/15 ring-1 ring-primary-foreground/25 backdrop-blur">
						<ShieldCheck className="size-6" />
					</div>
					<p className="text-xs font-semibold uppercase tracking-[0.2em] text-primary-foreground/70">
						Protected sign-in
					</p>
					<h1 className="mt-2 text-2xl font-semibold tracking-tight">
						One more check
					</h1>
					<p className="mt-2 max-w-sm text-sm leading-6 text-primary-foreground/75">
						Enter the code from your authenticator, or use one of your one-time
						recovery codes.
					</p>
				</div>

				<form onSubmit={submit} className="space-y-6 px-8 py-8">
					<div className="space-y-2">
						<label htmlFor="mfa-code" className="text-sm font-medium">
							Authentication code
						</label>
						<div className="relative">
							<KeyRound className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
							<Input
								ref={inputRef}
								id="mfa-code"
								name="code"
								inputMode="numeric"
								autoComplete="one-time-code"
								value={code}
								onChange={(event) => setCode(event.target.value)}
								disabled={submitting}
								placeholder="000 000"
								aria-invalid={Boolean(error)}
								className="h-12 rounded-xl pl-10 text-center font-mono text-lg tracking-[0.3em]"
							/>
						</div>
						<p className="text-xs text-muted-foreground">
							This login request expires after five minutes.
						</p>
					</div>

					{error && (
						<div
							role="alert"
							className="flex gap-2 rounded-xl border border-destructive/20 bg-destructive/10 p-3 text-sm text-destructive"
						>
							<AlertCircle className="mt-0.5 size-4 shrink-0" />
							<span>{error}</span>
						</div>
					)}

					<div className="space-y-3">
						<Button
							type="submit"
							size="lg"
							className="h-11 w-full rounded-xl"
							disabled={submitting || code.replace(/[\s-]/g, "").length < 6}
						>
							{submitting ? (
								<Loader2 className="animate-spin" />
							) : (
								<ShieldCheck />
							)}
							Verify and continue
						</Button>
						<div className="relative py-1">
							<div className="absolute inset-0 flex items-center">
								<span className="w-full border-t" />
							</div>
							<div className="relative flex justify-center text-[11px] uppercase tracking-wider">
								<span className="bg-card px-2 text-muted-foreground">or</span>
							</div>
						</div>
						<Button
							type="button"
							variant="outline"
							size="lg"
							className="h-11 w-full rounded-xl"
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
			</section>
		</main>
	);
}
