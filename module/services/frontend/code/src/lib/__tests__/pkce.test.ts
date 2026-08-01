import { describe, expect, it } from "vitest";
import { newPkce } from "../auth";

// RFC 7636 §4.2: code_challenge = BASE64URL-ENCODE(SHA256(ASCII(code_verifier)))
//
// The provider only ever receives the verifier *string*, so it can only re-hash
// that string. An implementation that hashes the random bytes the verifier was
// encoded from produces a challenge nobody can reproduce, and every exchange
// fails with "invalid_grant: Invalid code verifier" — after a full round trip to
// the provider, which makes it look like a credential or redirect problem.
async function expectedChallenge(verifier: string): Promise<string> {
	const digest = await crypto.subtle.digest(
		"SHA-256",
		new TextEncoder().encode(verifier),
	);
	let binary = "";
	const bytes = new Uint8Array(digest);
	for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
	return btoa(binary)
		.replace(/\+/g, "-")
		.replace(/\//g, "_")
		.replace(/=+$/, "");
}

describe("PKCE", () => {
	it("derives the challenge from the verifier string, per RFC 7636", async () => {
		const { verifier, challenge } = await newPkce();
		expect(challenge).toBe(await expectedChallenge(verifier));
	});

	it("produces a verifier within the RFC length and charset limits", async () => {
		const { verifier } = await newPkce();
		expect(verifier.length).toBeGreaterThanOrEqual(43);
		expect(verifier.length).toBeLessThanOrEqual(128);
		// Unreserved characters only — base64url must not leak + / or padding.
		expect(verifier).toMatch(/^[A-Za-z0-9._~-]+$/);
	});

	it("produces a base64url challenge with no padding", async () => {
		const { challenge } = await newPkce();
		expect(challenge).toMatch(/^[A-Za-z0-9_-]+$/);
	});

	it("is unpredictable across calls", async () => {
		const [first, second] = await Promise.all([newPkce(), newPkce()]);
		expect(first.verifier).not.toBe(second.verifier);
		expect(first.challenge).not.toBe(second.challenge);
	});
});
