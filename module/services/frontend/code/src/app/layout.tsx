import type { Metadata } from "next";
import { ConsentBanner } from "@/components/consent-banner";
import { Providers } from "@/lib/providers";
import "./globals.css";
import { Geist } from "next/font/google";
import { appearanceStyleProperties } from "@/lib/appearance";
import { cn } from "@/lib/utils";
import frontendConfig from "../../frontend.config";

const geist = Geist({ subsets: ["latin"], variable: "--font-sans" });

export const metadata: Metadata = {
	title: frontendConfig.branding.title,
	description: frontendConfig.branding.description,
	...(frontendConfig.branding.favicon
		? { icons: { icon: frontendConfig.branding.favicon } }
		: {}),
};

export default function RootLayout({
	children,
}: {
	children: React.ReactNode;
}) {
	return (
		<html
			lang="en"
			suppressHydrationWarning
			className={cn("font-sans", geist.variable)}
			style={appearanceStyleProperties(frontendConfig.appearance)}
		>
			<body className="antialiased">
				<Providers frontendConfig={frontendConfig}>
					{children}
					<ConsentBanner />
				</Providers>
			</body>
		</html>
	);
}
