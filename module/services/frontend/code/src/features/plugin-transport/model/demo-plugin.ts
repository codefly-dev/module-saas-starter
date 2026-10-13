import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
	isFrontendServicePath,
	resolveConnectProcedure,
} from "@codefly-dev/saas-plugin-contract";

/**
 * The demo plugin the transport playground drives: two service requirements, in
 * the two shapes a module's bindings come in, and the routes each one declares.
 *
 * It is a declaration, not an installation. `frontend.config.ts` does not
 * compose it, so no product surface changes and no alias becomes reachable by
 * existing it — the playground sends its routes through the host's real
 * transport and shows whatever the host answers, which for an alias nobody
 * installed is `plugin_service_not_found`. What the page proves either way is
 * where each route falls: a route the transport cannot carry is refused in the
 * browser, before any request leaves it.
 */
export const transportDemoPlugin = definePlugin({
	contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
	name: "transport-demo",
	services: [
		{
			alias: "records",
			protocol: "rest",
			routePrefix: "/v1/demo",
			compatibility: { contract: "demo.records", major: 1 },
		},
		{
			// A Connect requirement declares the origin root, because that is
			// where the Connect protocol puts a procedure — and where this
			// contract's own capability handshake has always been probed. It
			// also names the services it may reach: a procedure's path carries
			// its own service name, so without that list the alias would reach
			// every service the upstream origin serves.
			alias: "procedures",
			protocol: "connect",
			routePrefix: "/",
			connectServices: ["demo.procedures.v1.RecordService"],
			compatibility: { contract: "demo.procedures", major: 1 },
		},
		{
			// The same backend mounted under a path prefix rather than at the
			// origin root. The prefix says where Connect is mounted; the
			// service list still says which services the alias speaks to.
			alias: "mounted",
			protocol: "connect",
			routePrefix: "/v1/connect",
			connectServices: ["demo.procedures.v1.RecordService"],
			compatibility: { contract: "demo.procedures", major: 1 },
		},
	],
});

export type TransportDemoRoute = {
	/** The alias of `transportDemoPlugin` this route belongs to. */
	readonly alias: string;
	readonly method: "GET" | "POST";
	/** The relative path, as a product passes it to the transport. */
	readonly path: string;
	/** What the shape is, in the vocabulary a module's bindings use. */
	readonly shape: string;
	/**
	 * Whether the transport carries this shape at all. A refused row is here on
	 * purpose: a playground that only showed the admitted shapes could not show
	 * that the gate still closes.
	 */
	readonly carried: boolean;
};

/**
 * Every shape a module's HTTP bindings produce, including the four custom-verb
 * forms (AIP-136) that gave this page its reason to exist, and the refusals
 * that bound them.
 */
export const TRANSPORT_DEMO_ROUTES: readonly TransportDemoRoute[] = [
	{
		alias: "records",
		method: "GET",
		path: "records",
		shape: "collection read",
		carried: true,
	},
	{
		alias: "records",
		method: "GET",
		path: "records/record-1",
		shape: "item read",
		carried: true,
	},
	{
		alias: "records",
		method: "POST",
		path: "records:recover",
		shape: "custom verb on a collection",
		carried: true,
	},
	{
		alias: "records",
		method: "POST",
		path: "records/record-1/commands:recover",
		shape: "custom verb on a sub-collection",
		carried: true,
	},
	{
		alias: "records",
		method: "POST",
		path: "records/record-1/interactions/interaction-1:respond",
		shape: "custom verb on an item",
		carried: true,
	},
	{
		alias: "records",
		method: "POST",
		path: "triggers/nightly:fire",
		shape: "custom verb on a named resource",
		carried: true,
	},
	{
		alias: "procedures",
		method: "POST",
		path: "demo.procedures.v1.RecordService/RecoverRecord",
		shape: "connect procedure at the origin root",
		carried: true,
	},
	{
		alias: "mounted",
		method: "POST",
		path: "demo.procedures.v1.RecordService/RecoverRecord",
		shape: "connect procedure under a mount prefix",
		carried: true,
	},
	{
		alias: "procedures",
		method: "POST",
		path: "demo.other.v1.SecretService/Read",
		shape: "a procedure of a service the requirement never declared",
		carried: false,
	},
	{
		alias: "records",
		method: "POST",
		path: "records:recover/commands",
		shape: "a colon before the last segment",
		carried: false,
	},
	{
		alias: "records",
		method: "GET",
		path: "records/../secrets",
		shape: "a traversal out of the declared prefix",
		carried: false,
	},
	{
		alias: "records",
		method: "POST",
		path: "..:recover",
		shape: "a traversal wearing a custom verb",
		carried: false,
	},
	{
		alias: "procedures",
		method: "POST",
		path: "records",
		shape: "a REST path on a connect alias",
		carried: false,
	},
];

/**
 * Whether the transport carries one demo route, derived from the contract's own
 * predicates rather than from the row's `carried` flag — so the table and the
 * transport cannot disagree about a shape while both look right.
 */
export function transportCarries(route: TransportDemoRoute): boolean {
	const segments = route.path.split("/");
	if (!isFrontendServicePath(segments)) return false;
	const alias = transportDemoPlugin.services?.find(
		(service) => service.alias === route.alias,
	);
	return alias?.protocol === "connect"
		? resolveConnectProcedure(alias.connectServices ?? [], segments) !== null
		: true;
}

/**
 * Whether sending this route would write upstream. HTTP's own reading: GET is
 * safe and idempotent, anything else is not — derived rather than flagged per
 * row, so a row added later cannot forget to declare itself dangerous.
 *
 * It matters because the page's plugin name is the operator's to edit. Pointed
 * at a plugin their composition really installed, a press on one of these rows
 * is a real write to that backend under the operator's own credential, so the
 * page asks first. A route the transport will not carry needs no confirmation:
 * nothing leaves the browser.
 */
export function transportMutates(route: TransportDemoRoute): boolean {
	return route.method !== "GET" && transportCarries(route);
}
