import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";
import { SiteFooter, SiteHeader } from "@/components/site-shell";
import { marketingEnvironment } from "@/config/environment";
import { siteConfig } from "@/config/site";
import "./globals.css";

export const metadata: Metadata = {
  metadataBase: new URL(siteConfig.urls.marketing),
  title: {
    default: siteConfig.company.productName,
    template: `%s · ${siteConfig.company.productName}`,
  },
  description: siteConfig.company.shortDescription,
  applicationName: siteConfig.company.productName,
  alternates: { canonical: "/" },
  icons: { icon: siteConfig.brand.favicon },
  openGraph: {
    type: "website",
    siteName: siteConfig.company.productName,
    title: siteConfig.company.productName,
    description: siteConfig.company.shortDescription,
    url: "/",
    images: [
      {
        url: "/og.png",
        width: 1200,
        height: 630,
        alt: "One starter. Two deployables: public company site and authenticated product.",
      },
    ],
  },
  twitter: {
    card: "summary_large_image",
    title: siteConfig.company.productName,
    description: siteConfig.company.shortDescription,
    images: ["/og.png"],
  },
  robots: marketingEnvironment().indexable
    ? { index: true, follow: true }
    : { index: false, follow: false, nocache: true },
};

export const viewport: Viewport = {
  colorScheme: "light",
  themeColor: siteConfig.brand.colors.background,
};

export default function RootLayout({ children }: { children: ReactNode }) {
  const environment = marketingEnvironment();
  return (
    <html lang={siteConfig.locales.default}>
      <body
        style={
          {
            "--brand-primary": siteConfig.brand.colors.primary,
            "--brand-accent": siteConfig.brand.colors.accent,
            "--brand-background": siteConfig.brand.colors.background,
            "--brand-foreground": siteConfig.brand.colors.foreground,
            "--font-sans": siteConfig.brand.typography.sans,
            "--font-heading": siteConfig.brand.typography.heading,
          } as React.CSSProperties
        }
      >
        <a className="skip-link" href="#main-content">
          Skip to main content
        </a>
        <SiteHeader />
        <main id="main-content" tabIndex={-1}>
          {environment.enabled ? children : (
            <PageDisabled />
          )}
        </main>
        <SiteFooter />
      </body>
    </html>
  );
}

function PageDisabled() {
  return (
    <section className="page-intro shell narrow-shell">
      <p className="eyebrow">Marketing disabled</p>
      <h1>This public service is disabled by configuration.</h1>
      <p className="lede">
        The authenticated product remains independently available at{" "}
        <a href={siteConfig.urls.app}>{siteConfig.urls.app}</a>.
      </p>
    </section>
  );
}
