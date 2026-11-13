// Team casting: viewing the product as a team's member sees it, on every host
// that serves the session interface. The data and the acts come through a
// CastHost: `sessionCastHost` over the generated host.session.v1 client (a
// host that holds bearers adopts the cast's), `edgeCastHost` for the local
// authority's edge (the cast is the browser's cookie).
export {
	type Casting,
	CastBar,
	CastingProvider,
	HoldingBadges,
	holdingLabel,
	openTeamSwitcher,
	TeamSwitcher,
	useCasting,
	ViewAsTeamButton,
} from "./casting.js";
export {
	type CastHost,
	type CastSession,
	type CastTeam,
	edgeCastHost,
	type Holding,
	refusalText,
	type SessionCastHostOptions,
	sessionCastHost,
	signedOut,
} from "./host.js";
export { type CastRefusal, forgetCastTeams } from "./store.js";
