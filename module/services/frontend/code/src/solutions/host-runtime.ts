import { version as reactVersion } from "react";

/**
 * What this host offers a runtime-loaded solution remote, stated as data so the
 * registration path can REFUSE an incompatible remote before its code is ever
 * fetched — rather than storing a declared requirement and hoping it holds.
 *
 * It lives outside SolutionOutlet because the register route (server) and the
 * Module-Federation host (client) must agree on one set of numbers, and a route
 * handler cannot pull in the client component's kit imports to read them.
 */

/**
 * Major of the registration manifest wire shape. A solution built against a
 * different major is not describing the same document, so nothing further about
 * it can be trusted to mean what it says.
 */
export const SOLUTION_MANIFEST_SCHEMA_MAJOR = 1;

/**
 * Major of the host↔remote runtime contract: the `SolutionPageProps` the host
 * injects (api base, token accessors, dashboard authoring) and the `./Page`
 * default export it expects back. Widening it is a minor; changing or removing
 * anything a remote already receives is a major.
 */
export const SOLUTION_HOST_CONTRACT_MAJOR = 1;

/**
 * The UI kit (`@codefly-dev/ui` + `@codefly-dev/saas-ui`) ships lockstep with this
 * host, so one version covers both. It MUST track the packages' real version — a
 * shared module that under-reports its version can lose singleton resolution to
 * a remote that bundles a higher one, splitting the instance the dedup exists to
 * keep single. The `kit-shared-version` test pins this to the packages' actual
 * versions so a bump can't drift it silently.
 */
export const CODEFLY_KIT_VERSION = "0.2.1";

/**
 * `@codefly-dev/saas-sdk` tracks the published accounts/connect API contract,
 * not the UI kit's release cadence, so it versions independently of the kit. Its
 * shared version MUST still match the package's real version (same
 * `kit-shared-version` invariant, checked per package rather than against one
 * shared constant).
 */
export const CODEFLY_SAAS_SDK_VERSION = "0.2.3";

/**
 * The exact versions this host publishes into the Module-Federation shared
 * scope. A remote declares the range it needs of each; registration checks the
 * range against these. React is read from the running package rather than
 * restated, so a dependency bump cannot leave a stale number here.
 */
export const HOST_SHARED_VERSIONS: Readonly<Record<string, string>> = {
	react: reactVersion,
	"react-dom": reactVersion,
	"react/jsx-runtime": reactVersion,
	"@codefly-dev/ui": CODEFLY_KIT_VERSION,
	"@codefly-dev/saas-ui": CODEFLY_KIT_VERSION,
	"@codefly-dev/saas-sdk": CODEFLY_SAAS_SDK_VERSION,
};

/** The React version every remote renders against (the host owns the instance). */
export const HOST_REACT_VERSION = reactVersion;
