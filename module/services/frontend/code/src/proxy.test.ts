/**
 * The login-redirect decision, tested directly.
 *
 * Everything here is about one measured failure: a discovery request answered
 * with a 307 to `/auth/login`. A client that cannot read metadata cannot
 * authenticate, and the symptom it reports — "this host is not an authorization
 * server" — points nowhere near the middleware that caused it.
 */

import { describe, expect, it } from "vitest";
import { HOST_BROWSER_PROCEDURES } from "@/lib/host-browser-procedures";
import { isPublic } from "./proxy";

describe("public paths", () => {
	it("exempts every well-known path, including ones this host does not serve", () => {
		for (const pathname of [
			// The RFC 8414 document a client reads to find the authorization
			// server. This is the one that was measured redirecting to login.
			"/.well-known/oauth-authorization-server",
			// RFC 9728 builds the protected-resource URL by INSERTING the
			// well-known segment before the resource's own path, so a client that
			// constructs it rather than reading the challenge arrives here.
			"/.well-known/oauth-protected-resource",
			"/.well-known/oauth-protected-resource/api/solutions/example/proxy/mcp",
			// Not served by this host at all. It must still reach the handler that
			// answers 404: "no document here" is a true answer a client can act
			// on, and a login page is not. Being public is not the same as
			// existing, and conflating them is what produced the redirect.
			"/.well-known/openid-configuration",
			"/.well-known/anything-at-all",
			// Not at the root. A solution's protected-resource metadata sits
			// beneath its own route, and `/solutions/<id>/` is a page of this
			// app — so a prefix test on "/.well-known/" would leave exactly the
			// namespace a client reads during discovery behind the redirect.
			"/solutions/example/.well-known/oauth-protected-resource",
			"/solutions/example/.well-known/anything",
		]) {
			expect(isPublic(pathname), pathname).toBe(true);
		}
	});

	it("still protects the pages it protected before", () => {
		// Without this the test above would pass against a predicate that simply
		// returned true, which would be the same defect with the opposite sign.
		// Not "/" — the landing page is deliberately public.
		for (const pathname of ["/settings", "/organizations", "/oauth2/consent"]) {
			expect(isPublic(pathname), pathname).toBe(false);
		}
	});

	it("keeps the authorization endpoint public and the consent page private", () => {
		// `/oauth2/authorize` is where an unauthenticated person is SUPPOSED to
		// arrive, so it is public and renders its own sign-in. Consent is the
		// opposite: it may only be reached by someone already signed in, so it
		// stays behind the redirect.
		expect(isPublic("/oauth2/authorize")).toBe(true);
		expect(isPublic("/oauth2/consent")).toBe(false);
	});

	it("forwards the signed-in member’s held installation roles to the authority", () => {
		expect(isPublic("/host.installation.v1.InstallationService/ListHeld")).toBe(true);
		expect(isPublic("/host.installation.v1.InstallationService/ListHeldExtra")).toBe(false);
	});

	it("forwards the host procedures a browser of this app calls, and no other", () => {
		// The solution Setup tab reads and changes role grants through the host
		// installation interface, and the chrome reads the session interface's
		// browser edge, both as the signed-in person: the gateway decides.
		for (const pathname of HOST_BROWSER_PROCEDURES) {
			expect(isPublic(pathname), pathname).toBe(true);
		}
		// What no browser of this app calls is not forwarded: installing stays
		// behind the host's consent form, the installation interface's Current
		// is a module's call, a person signs in through /v1/auth/*, and nothing
		// else of the package gains a route.
		for (const pathname of [
			"/host.installation.v1.InstallationService/Install",
			"/host.installation.v1.InstallationService/Current",
			"/host.installation.v1.InstallableService/Install",
			"/host.session.v1.SessionService/SignIn",
			"/host.session.v1.SessionService/ListFixturePeople",
			"/host.authority.v1.AuthorityService/Issue",
		]) {
			expect(isPublic(pathname), pathname).toBe(false);
		}
	});

	it("reads the session interface's browser edge, which decides whether admin actions are offered", () => {
		// The regression this names: the auth provider reads Current to learn
		// whether the bearer is a cast, and an unconfirmed session is never
		// presented as the person. With no route, Current answers 404 from this
		// app and every administrator action is withheld from everyone.
		expect(isPublic("/host.session.v1.SessionService/Current")).toBe(true);
		expect(isPublic("/host.session.v1.SessionService/OpenRoleView")).toBe(true);
		expect(isPublic("/host.session.v1.SessionService/OpenTeamCast")).toBe(true);
	});
});
