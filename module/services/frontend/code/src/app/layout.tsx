import type { Metadata } from "next";
import { headers } from "next/headers";
import { ConsentBanner } from "@/components/consent-banner";
import { appearanceStyleProperties } from "@/lib/appearance";
import { Providers } from "@/lib/providers";
import { resolveSkin, shouldResolveHost } from "@/lib/skin";
import "./globals.css";
import frontendConfig from "../../frontend.config";

/**
 * Runtime skins require dynamic rendering. The image is built without the skin
 * (it arrives as a mounted ConfigMap / env at container start), so a
 * prerendered layout would bake the compiled default instead of resolving the
 * mounted skin per request. Reading request headers is what opts a route into
 * dynamic rendering. `shouldResolveHost` decides when to read them: the
 * build-time flag FRONTEND_SKIN_RUNTIME=1 forces the read (hence dynamic
 * rendering) while building a skinnable image, and at runtime the presence of a
 * mounted source keeps per-host resolution running — the flag is not inlined
 * into the server bundle, so gating on it alone would resolve the host at build
 * but never at request time. A plain starter build sets neither and stays static.
 */
async function currentSkin() {
	const host = shouldResolveHost() ? (await headers()).get("host") : null;
	return resolveSkin({ fallback: frontendConfig, host });
}

export async function generateMetadata(): Promise<Metadata> {
	const { branding } = await currentSkin();
	return {
		title: branding.title,
		description: branding.description,
		...(branding.favicon ? { icons: { icon: branding.favicon } } : {}),
	};
}

export default async function RootLayout({
	children,
}: {
	children: React.ReactNode;
}) {
	const skin = await currentSkin();
	return (
		<html
			lang="en"
			suppressHydrationWarning
			className="font-sans"
			style={appearanceStyleProperties(skin.appearance)}
		>
			<body className="antialiased">
				<Providers
					frontendConfig={{
						...frontendConfig,
						branding: skin.branding,
						appearance: skin.appearance,
					}}
				>
					{children}
					<ConsentBanner />
				</Providers>
			</body>
		</html>
	);
}
