import { resolveFrontendAppearance } from "@codefly/saas-plugin-contract";
import type { RawSkinDescriptor } from "@codefly-dev/ui/skin";
import { appearanceStyleProperties } from "@/lib/appearance";

// External explorer adapter: the same validator and CSS projection the product
// applies at SSR, so a skin that renders here renders the same in the app.
//
// Rebuilt against the current kit. The previous adapter carried the
// component-remotes plumbing (bindSkinRemotes, parseComponentRemote,
// ComponentSkins); the kit retired that feature — its skin types file records
// `remotes` as a field that validated and resolved to nothing — so the adapter
// no longer wires it.
export function resolve(descriptor: RawSkinDescriptor) {
	const appearance = resolveFrontendAppearance(descriptor.appearance);
	return {
		properties: appearanceStyleProperties(appearance),
		defaultTheme: appearance.defaultTheme,
	};
}
