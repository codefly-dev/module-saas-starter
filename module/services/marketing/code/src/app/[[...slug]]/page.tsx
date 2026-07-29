import type { Metadata } from "next";
import { siteConfig } from "@/config/site";
import {
  marketingRouteInventory,
  localizedPath,
  metadataForRoute,
  renderMarketingPage,
  resolveLocalizedRoute,
} from "@/lib/page-renderer";

type PageProps = {
  params: Promise<{ slug?: string[] }>;
};

export const revalidate = 300;
export const dynamicParams = false;

export async function generateStaticParams() {
  return (await marketingRouteInventory()).map((slug) => ({ slug }));
}

export async function generateMetadata({
  params,
}: PageProps): Promise<Metadata> {
  const { slug = [] } = await params;
  const localizedRoute = resolveLocalizedRoute(slug);
  const route = localizedPath(
    localizedRoute.segments,
    localizedRoute.locale,
  );
  const publishedRoutes = new Set(
    (await marketingRouteInventory()).map(
      (segments) => `/${segments.join("/")}`,
    ),
  );
  const metadata = await metadataForRoute(slug);
  return {
    title: metadata.title,
    description: metadata.description,
    alternates: {
      canonical: route,
      languages: Object.fromEntries(
        siteConfig.locales.enabled
          .map(
            (locale) =>
              [
                locale,
                localizedPath(localizedRoute.segments, locale),
              ] as const,
          )
          .filter(([, path]) => publishedRoutes.has(path)),
      ),
    },
    openGraph: {
      title: metadata.title,
      description: metadata.description,
      url: route,
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
      title: metadata.title,
      description: metadata.description,
      images: ["/og.png"],
    },
  };
}

export default async function MarketingPage({ params }: PageProps) {
  const { slug = [] } = await params;
  return renderMarketingPage({ segments: slug });
}
