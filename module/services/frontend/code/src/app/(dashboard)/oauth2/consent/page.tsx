import { ConsentPage } from "@/features/auth/ui/consent-page";

export const dynamic = "force-dynamic";

/**
 * Where a person approves a client by name (issue #1003).
 *
 * It sits in the `(dashboard)` group, which is what makes the generated page
 * catalog record it as AUTHENTICATED — and that is the truth: approving a
 * client is an act of the person, and there is nobody to attribute it to before
 * they have signed in. The `(auth)` group would have recorded it as public,
 * which would be a false statement in a generated authorization artifact about
 * a page the middleware in fact gates.
 *
 * Rendering inside the product shell is deliberate rather than incidental. The
 * person is signed in by the time they arrive, and seeing WHICH account and
 * organization they are signed in as is part of the decision they are being
 * asked to make.
 */
export default function Page() {
	return <ConsentPage />;
}
