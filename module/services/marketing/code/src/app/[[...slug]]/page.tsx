import type { Metadata } from "next";
import { siteConfig } from "@/config/site";
import {
  marketingRouteInventory,
  metadataForRoute,
  renderMarketingPage,
} from "@/lib/page-renderer";
import type { AttributionInput } from "@/lib/cta";

type PageProps = {
  params: Promise<{ slug?: string[] }>;
  searchParams: Promise<AttributionInput>;
};

export const revalidate = 300;

export async function generateStaticParams() {
  return (await marketingRouteInventory()).map((slug) => ({ slug }));
}

export async function generateMetadata({
  params,
}: PageProps): Promise<Metadata> {
  const { slug = [] } = await params;
  const route = `/${slug.join("/")}`;
  const metadata = await metadataForRoute(slug);
  return {
    title: metadata.title,
    description: metadata.description,
    alternates: {
      canonical: route,
      languages: Object.fromEntries(
        siteConfig.locales.enabled.map((locale) => [locale, route]),
      ),
    },
    openGraph: {
      title: metadata.title,
      description: metadata.description,
      url: route,
    },
    twitter: { title: metadata.title, description: metadata.description },
  };
}

export default async function MarketingPage({
  params,
  searchParams,
}: PageProps) {
  const [{ slug = [] }, query] = await Promise.all([params, searchParams]);
  return renderMarketingPage({ segments: slug, searchParams: query });
}
