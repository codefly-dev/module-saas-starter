import type { Metadata } from "next";
import { headers } from "next/headers";
import { ConsentBanner } from "@/components/consent-banner";
import { appearanceStyleProperties } from "@/lib/appearance";
import { Providers } from "@/lib/providers";
import { resolveSkin, skinResolutionEnabled } from "@/lib/skin";
import "./globals.css";
import frontendConfig from "../../frontend.config";

/**
 * Resolve the skin for this request. When no runtime source is configured the
 * compiled default renders and we avoid reading headers, so static rendering is
 * preserved; when a source (CMS/file/env) is configured the skin is resolved
 * per host at SSR — validated and flash-free, since the tokens land on <html>.
 */
async function currentSkin() {
	const host = skinResolutionEnabled()
		? (await headers()).get("host")
		: null;
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
