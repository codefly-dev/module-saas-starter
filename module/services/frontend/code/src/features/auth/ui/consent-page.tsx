"use client";

import { AlertCircle, Check, ShieldCheck, X } from "lucide-react";
import { useMemo, useRef, useState, useSyncExternalStore } from "react";
import { BrandMark } from "@/components/brand-mark";
import {
	declineClientAuthorization,
	grantClientAuthorization,
	PENDING_REQUEST_KEY,
	PENDING_RESOLUTION_KEY,
	parsePendingClientAuthorization,
	parsePendingClientResolution,
} from "@/features/auth/model/client-authorization";
import { useAppearance } from "@/lib/appearance-provider";
import { useAuth } from "@/lib/auth";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	ErrorState,
	Page,
	PageHeader,
	Panel,
	Spinner,
} from "@/shared/ui";

// sessionStorage is an external store, and this page's whole job is to render
// what is in it. useSyncExternalStore is how React reads one: it takes a server
// snapshot (nothing is readable there) and the client's, so the value arrives
// without an effect that sets state — which the react-hooks rule rejects, and
// rightly: a setState in an effect body is a second render for a value that was
// already available.
//
// The store does not change under this page — one pending request is written
// before the browser arrives and cleared when it leaves — so there is nothing
// to subscribe to. The unsubscribe is still returned, because useSyncExternalStore
// calls it.
const neverChanges = () => () => {};

function useSessionItem(key: string): string | null {
	return useSyncExternalStore(
		neverChanges,
		// Returns a string or null, both compared by value, so the snapshot is
		// referentially stable and cannot loop. The read is guarded because
		// sessionStorage access throws outright in some privacy modes.
		() => {
			try {
				return sessionStorage.getItem(key);
			} catch {
				return null;
			}
		},
		() => null,
	);
}

/**
 * The consent screen. It names the client and, when the request bound one, the
 * resource — "this client, for that one solution" is a different sentence from
 * "this client", and the difference is what the person is being asked to
 * decide.
 *
 * Everything it displays comes from what the HOST said about the request
 * (`pendingClientResolution`), never from the query string: a page that
 * rendered the client's own name from a URL parameter would let any site put
 * any name in front of the person. The host resolved the name from the operator
 * registry, or from the metadata document it fetched and validated itself.
 *
 * The host asks for consent in two cases, and says which applies:
 *   - a client the operator never declared, which registered itself by
 *     publishing a metadata document — nobody but the person can vouch for it;
 *   - a request that narrows the credential to one named resource.
 * An operator-declared client asking for nothing in particular never reaches
 * this page; the deployment already vouched for it.
 */
export function ConsentPage() {
	const { accessToken, isLoading, isAuthenticated } = useAuth();
	const { branding } = useAppearance();
	const [deciding, setDeciding] = useState<"allow" | "deny" | null>(null);
	const [error, setError] = useState<string | null>(null);
	const pending = useRef(false);

	const storedRequest = useSessionItem(PENDING_REQUEST_KEY);
	const storedResolution = useSessionItem(PENDING_RESOLUTION_KEY);
	// Parsed from the stable raw snapshots, so the objects below are rebuilt only
	// when what is stored actually changes.
	const request = useMemo(
		() => parsePendingClientAuthorization(storedRequest),
		[storedRequest],
	);
	const resolution = useMemo(
		() => parsePendingClientResolution(storedResolution),
		[storedResolution],
	);

	const clientName = resolution?.clientName ?? request?.clientId ?? "";
	// The only identity fact the host verified. Never the document's claim.
	const verifiedOrigin = resolution?.clientOrigin ?? "";
	const unvetted = resolution?.clientSource === "metadata_document";
	const resourceName = useMemo(() => {
		if (!resolution?.resource) return null;
		return resolution.resourceName || resolution.resource;
	}, [resolution]);

	async function allow() {
		if (!request || pending.current) return;
		pending.current = true;
		setError(null);
		setDeciding("allow");
		try {
			// The person pressed Allow: this is the one call site that may say so.
			await grantClientAuthorization(request, accessToken, {
				consentGranted: true,
			});
		} catch (err) {
			pending.current = false;
			setDeciding(null);
			// Said in this page's own words whatever was thrown: the cause is the
			// host's or the network's, and neither's text is for the person.
			console.error("The application could not be authorized", err);
			setError(
				"You are signed in, but the application could not be authorized. Try again, or cancel and start again from the application.",
			);
		}
	}

	function deny() {
		if (!request || pending.current) return;
		pending.current = true;
		setDeciding("deny");
		declineClientAuthorization(request);
	}

	// The hydration render has the server snapshot (no request) and the auth
	// provider has not finished exchanging the refresh cookie, so both reduce to
	// the same honest answer: this page does not know yet.
	if (isLoading) {
		return (
			<Page className="flex min-h-screen w-full items-center justify-center p-4">
				<Card className="w-full max-w-md">
					<CardHeader>
						<PageHeader title="Application access" />
					</CardHeader>
					<CardContent>
						<Spinner label="Loading approval request" />
					</CardContent>
				</Card>
			</Page>
		);
	}

	// No pending request, no validated resolution, or no session to attribute
	// the approval to. All three are the same thing from here: there is nothing
	// this page can put in front of the person to decide about.
	//
	// The resolution is what the HOST said about this client — its verified
	// origin, the resource, whether it is one the operator installed. Without
	// it the page would render Allow next to a name and nothing else: no
	// verified origin, no resource, no uninstalled-client warning, and clicking
	// it would still produce a code. An approval given against a blank
	// presentation is not an approval of anything, so it is refused and the
	// person is told to start again.
	if (!request || !resolution || !isAuthenticated) {
		return (
			<Page className="flex min-h-screen w-full items-center justify-center p-4">
				<Card className="w-full max-w-md">
					<CardHeader>
						<PageHeader title="Application access" />
					</CardHeader>
					<CardContent>
						<ErrorState
							title="Nothing to approve"
							detail="There is no sign-in request to approve here, or its details could not be confirmed. Start again from the application you were using."
						/>
					</CardContent>
				</Card>
			</Page>
		);
	}

	return (
		<Page className="flex min-h-screen w-full items-center justify-center p-4">
			<div className="w-full max-w-md space-y-6">
				<div className="flex items-center justify-center gap-3">
					<BrandMark
						className="flex size-10 items-center justify-center overflow-hidden rounded-xl bg-primary text-lg font-bold text-primary-foreground"
						imageClassName="p-1"
					/>
					<span className="text-xl font-semibold tracking-tight">
						{branding.name}
					</span>
				</div>

				<Card>
					<CardHeader>
						<PageHeader title={`Allow ${clientName}?`} />
						{/* The name above comes from the client's own document and is
						    not verified. The origin here is: it is where the host
						    actually fetched that document from, and it is shown
						    unconditionally so a trusted-looking name can never be the
						    only thing the person sees. CIMD draft-02 §8.5. */}
						{verifiedOrigin && (
							<CardDescription className="break-all">
								<code>{verifiedOrigin}</code>
							</CardDescription>
						)}
						<CardDescription>
							{resourceName
								? `It is asking to use ${resourceName} as you.`
								: "It is asking to act as you."}
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<Panel className="space-y-3 text-sm text-muted-foreground">
							<div className="flex items-start gap-2.5">
								<ShieldCheck className="mt-0.5 size-4 shrink-0" />
								<span>
									It will act with your own permissions — never more — and
									everything it does is recorded against both you and it.
								</span>
							</div>
							{resolution?.resource && (
								<div className="flex items-start gap-2.5">
									<Check className="mt-0.5 size-4 shrink-0" />
									<span>
										Its access is limited to{" "}
										<code className="break-all text-foreground">
											{resolution.resource}
										</code>
										.
									</span>
								</div>
							)}
							{unvetted && (
								<div className="flex items-start gap-2.5">
									<AlertCircle className="mt-0.5 size-4 shrink-0" />
									<span>
										This application was not installed by your administrator.
										The only thing checked about it is that its registration is
										published at{" "}
										<code className="break-all text-foreground">
											{resolution?.clientOrigin ?? request.clientId}
										</code>
										. Allow it only if you started this yourself.
									</span>
								</div>
							)}
						</Panel>

						{error && <ErrorState title={error} />}
					</CardContent>
					<CardFooter className="gap-2">
						<Button
							size="lg"
							className="flex-1"
							onClick={allow}
							disabled={deciding !== null}
						>
							{deciding === "allow" ? (
								<Spinner label="Authorizing application" />
							) : (
								<Check />
							)}
							Allow
						</Button>
						<Button
							variant="outline"
							size="lg"
							className="flex-1"
							onClick={deny}
							disabled={deciding !== null}
						>
							<X />
							Cancel
						</Button>
					</CardFooter>
				</Card>
			</div>
		</Page>
	);
}
