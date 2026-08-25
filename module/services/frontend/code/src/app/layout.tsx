import type { Metadata } from "next";
import { headers } from "next/headers";
import { ConsentBanner } from "@/components/consent-banner";
import { appearanceStyleProperties } from "@/lib/appearance";
import { Providers } from "@/lib/providers";
import { resolveSkin } from "@/lib/skin";
import "./globals.css";
import frontendConfig from "../../frontend.config";

/**
 * Runtime skins require dynamic rendering. The image is built without the skin
 * (it arrives as a mounted ConfigMap / env at container start), so a
 * prerendered layout would bake the compiled default instead of resolving the
 * mounted skin per request. Reading request headers is what opts a route into
 * dynamic rendering, so we gate that read on a BUILD-time flag,
 * FRONTEND_SKIN_RUNTIME=1 — set it when building a skinnable image and the
 * layout renders dynamically; omit it and a plain starter build stays static.
 * Which skin is resolved is a separate, runtime concern (FRONTEND_SKIN_DIR / …).
 */
const SKIN_RUNTIME = process.env.FRONTEND_SKIN_RUNTIME === "1";

async function currentSkin() {
	// Gate the header read on the build-time flag ONLY: at build there is no
	// mounted skin yet, so it must be the flag (not the presence of a source)
	// that opts the route into dynamic rendering. resolveSkin still returns the
	// compiled default when no runtime source is configured.
	const host = SKIN_RUNTIME ? (await headers()).get("host") : null;
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
