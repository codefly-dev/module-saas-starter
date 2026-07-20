import { useMutation } from "@tanstack/react-query";

export function useSendMagicLink() {
	return useMutation({
		mutationFn: async ({ email }: { email: string }) => {
			const res = await fetch("/v1/auth/magic-link", {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ email }),
			});
			if (!res.ok) {
				const body = await res.text().catch(() => "");
				throw new Error(body || "Failed to send magic link");
			}
			return res.json();
		},
	});
}

export function useVerifyMagicLink() {
	return useMutation({
		mutationFn: async ({ token }: { token: string }) => {
			const res = await fetch("/v1/auth/magic-link/verify", {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ token }),
			});
			if (!res.ok) {
				const body = await res.text().catch(() => "");
				throw new Error(body || "Invalid or expired magic link");
			}
			return res.json() as Promise<{
				accessToken: string;
				refreshToken: string;
				user?: { uuid: string };
				mfaRequired: boolean;
				mfaToken: string;
			}>;
		},
	});
}
