"use client";

import { useEffect, useRef } from "react";
import { configuredAbuseProtection } from "@/lib/abuse-protection";

declare global {
	interface Window {
		turnstile?: {
			render(
				container: HTMLElement,
				options: {
					sitekey: string;
					action: string;
					callback(token: string): void;
					"error-callback"(): void;
					"expired-callback"(): void;
				},
			): string;
			remove(widgetID: string): void;
		};
	}
}

const scriptID = "cloudflare-turnstile-script";

function loadTurnstile(): Promise<NonNullable<Window["turnstile"]>> {
	if (window.turnstile) return Promise.resolve(window.turnstile);
	return new Promise((resolve, reject) => {
		let script = document.getElementById(scriptID) as HTMLScriptElement | null;
		const ready = () =>
			window.turnstile
				? resolve(window.turnstile)
				: reject(new Error("Turnstile did not initialize"));
		if (!script) {
			script = document.createElement("script");
			script.id = scriptID;
			script.src =
				"https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
			script.async = true;
			script.defer = true;
			document.head.appendChild(script);
		}
		script.addEventListener("load", ready, { once: true });
		script.addEventListener(
			"error",
			() => reject(new Error("Turnstile could not be loaded")),
			{ once: true },
		);
	});
}

export function TurnstileWidget({
	action,
	onTokenChange,
}: {
	action: string;
	onTokenChange(token: string): void;
}) {
	const container = useRef<HTMLDivElement>(null);
	const configuration = configuredAbuseProtection(
		process.env.NEXT_PUBLIC_ABUSE_PROTECTION_MODE,
		process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY,
	);
	const enabled = configuration.enabled;
	const siteKey = enabled ? configuration.siteKey : "";

	useEffect(() => {
		if (!enabled || !container.current) {
			onTokenChange("");
			return;
		}
		let cancelled = false;
		let widgetID = "";
		loadTurnstile()
			.then((turnstile) => {
				if (cancelled || !container.current) return;
				widgetID = turnstile.render(container.current, {
					sitekey: siteKey,
					action,
					callback: onTokenChange,
					"error-callback": () => onTokenChange(""),
					"expired-callback": () => onTokenChange(""),
				});
			})
			.catch(() => onTokenChange(""));
		return () => {
			cancelled = true;
			if (widgetID) window.turnstile?.remove(widgetID);
		};
	}, [action, enabled, onTokenChange, siteKey]);

	if (!enabled) return null;
	return <div ref={container} aria-label="Bot protection challenge" />;
}
