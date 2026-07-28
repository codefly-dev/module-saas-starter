import Link from "next/link";
import { notFound } from "next/navigation";
import type { ReactNode } from "react";
import {
  AcquisitionCTA,
  EmptyState,
  PageIntro,
} from "@/components/site-shell";
import { acquisitionMode, siteConfig } from "@/config/site";
import {
  formatPlanAmount,
  loadPublicPlans,
  type PublicPlan,
} from "@/lib/catalog";
import {
  allPublicContent,
  type ContentDocument,
  type ContentKind,
  repositoryContentProvider,
} from "@/lib/content";
import { productHandoff, type AttributionInput } from "@/lib/cta";
import { Markdown } from "@/lib/markdown";

export type MarketingRoute = {
  segments: string[];
  searchParams: AttributionInput;
};

type IndexKind = "article" | "changelog" | "doc" | "use-case";

const featureCards = [
  {
    label: "Independent runtime",
    text: "Public content stays deployable and cacheable separately from authenticated product traffic.",
  },
  {
    label: "Catalog-backed pricing",
    text: "The pricing page reads a sanitized projection of the checkout catalog and degrades honestly when it is unavailable.",
  },
  {
    label: "Portable content",
    text: "Repository Markdown implements a narrow provider contract with validated publication metadata.",
  },
];

function DocumentCard({ document, href }: { document: ContentDocument; href: string }) {
  return (
    <article className="content-card">
      <div className="card-meta">
        <span>{document.category ?? document.type}</span>
        <time dateTime={document.publishedAt}>
          {new Intl.DateTimeFormat(document.locale, { dateStyle: "medium" }).format(
            new Date(document.publishedAt),
          )}
        </time>
      </div>
      <h2>
        <Link href={href}>{document.title}</Link>
      </h2>
      <p>{document.description}</p>
      <span className="text-link" aria-hidden="true">
        Read more →
      </span>
    </article>
  );
}

function ContentGrid({
  documents,
  prefix,
}: {
  documents: ContentDocument[];
  prefix: string;
}) {
  if (documents.length === 0) {
    return (
      <EmptyState
        title="Nothing published yet"
        description="Draft and scheduled content stays private until the publication workflow completes."
      />
    );
  }
  return (
    <div className="content-grid">
      {documents.map((document) => (
        <DocumentCard
          document={document}
          href={`${prefix}/${document.slug}`}
          key={`${document.locale}:${document.slug}`}
        />
      ))}
    </div>
  );
}

async function ContentIndex({
  kind,
  eyebrow,
  title,
  description,
  prefix,
  filter,
}: {
  kind: IndexKind;
  eyebrow: string;
  title: string;
  description: string;
  prefix: string;
  filter?: (document: ContentDocument) => boolean;
}) {
  let documents = await repositoryContentProvider.list(kind);
  if (filter) documents = documents.filter(filter);
  return (
    <>
      <PageIntro eyebrow={eyebrow} title={title} description={description} />
      <section className="shell section">
        <ContentGrid documents={documents} prefix={prefix} />
      </section>
    </>
  );
}

async function ContentDetail({
  kind,
  slug,
}: {
  kind: ContentKind;
  slug: string;
}) {
  const document = await repositoryContentProvider.get(kind, slug);
  if (!document) notFound();
  return (
    <article className="shell article-shell">
      <header className="article-header">
        <p className="eyebrow">{document.category ?? document.type}</p>
        <h1>{document.title}</h1>
        <p className="lede">{document.description}</p>
        <div className="byline">
          <span>By {document.author}</span>
          <span>Reviewed by {document.reviewer}</span>
          <time dateTime={document.updatedAt}>
            Updated{" "}
            {new Intl.DateTimeFormat(document.locale, {
              dateStyle: "long",
            }).format(new Date(document.updatedAt))}
          </time>
          <span>Revision {document.revision}</span>
        </div>
      </header>
      <Markdown source={document.body} />
    </article>
  );
}

function Home({ searchParams }: { searchParams: AttributionInput }) {
  const handoff = productHandoff(
    acquisitionMode() === "open_signup" ? "signup" : "login",
    searchParams,
  );
  return (
    <>
      <section className="hero shell">
        <div className="hero-copy">
          <p className="eyebrow">One starter. Two deployables.</p>
          <h1>Build the company people discover and the product they return to.</h1>
          <p className="lede">{siteConfig.company.longDescription}</p>
          <div className="button-row">
            {handoff ? (
              <a className="button" href={handoff}>
                Open the product
              </a>
            ) : null}
            <Link className="button button-quiet" href="/docs/getting-started">
              Read the launch guide
            </Link>
          </div>
        </div>
        <div className="hero-proof" aria-label="Runtime topology">
          <div className="topology-card topology-public">
            <span>www</span>
            <strong>Public company site</strong>
            <small>Static content · cached independently</small>
          </div>
          <div className="topology-connector" aria-hidden="true">
            Separate releases
          </div>
          <div className="topology-card topology-product">
            <span>app</span>
            <strong>Authenticated product</strong>
            <small>Sessions · tenants · administration</small>
          </div>
        </div>
      </section>
      <section className="shell section" aria-labelledby="foundation-heading">
        <div className="section-heading">
          <p className="eyebrow">Launch foundation</p>
          <h2 id="foundation-heading">Public primitives with explicit boundaries</h2>
        </div>
        <div className="feature-grid">
          {featureCards.map((feature, index) => (
            <article key={feature.label}>
              <span className="feature-number">0{index + 1}</span>
              <h3>{feature.label}</h3>
              <p>{feature.text}</p>
            </article>
          ))}
        </div>
      </section>
      <AcquisitionCTA attribution={searchParams} />
    </>
  );
}

function ProductPage() {
  return (
    <>
      <PageIntro
        eyebrow="Product overview"
        title="A customer product shell backed by serious service contracts."
        description="The public service introduces the product without importing its sessions, tenant state, database access, or admin clients."
      />
      <section className="shell section split-section">
        <div>
          <h2>Public by design</h2>
          <p>
            Brand configuration, publication metadata, acquisition handoff, and
            the public pricing projection are safe to render without product
            credentials.
          </p>
        </div>
        <ul className="check-list">
          <li>Independent build, health, release, and rollback</li>
          <li>Static and revalidated content with explicit cache policy</li>
          <li>Only approved attribution crosses into authentication</li>
          <li>No apex-scoped authentication cookie requirement</li>
        </ul>
      </section>
      <AcquisitionCTA />
    </>
  );
}

function AboutPage() {
  return (
    <>
      <PageIntro
        eyebrow="About"
        title={siteConfig.company.name}
        description={siteConfig.company.longDescription}
      />
      <section className="shell narrow-shell section prose">
        <h2>Development fixture</h2>
        <p>
          The checked-in company identity is intentionally generic. It is not a
          claim about a real company, customer, certification, or business
          result. Adopters configure their own public identity in one validated
          file and run the readiness report before launch.
        </p>
      </section>
    </>
  );
}

function ContactPage() {
  return (
    <>
      <PageIntro
        eyebrow="Contact"
        title="Talk to the right team."
        description="Public contact addresses come from validated brand configuration. No second support inbox or notification engine lives in this service."
      />
      <section className="shell section contact-grid">
        {[
          ["Sales", siteConfig.contacts.sales],
          ["Support", siteConfig.contacts.support],
          ["Privacy", siteConfig.contacts.privacy],
          ["Security", siteConfig.contacts.security],
        ].map(([label, address]) => (
          <article className="content-card" key={label}>
            <p className="eyebrow">{label}</p>
            <h2>
              {!address.endsWith(".invalid")
                ? `Contact ${label.toLowerCase()}`
                : "Not configured"}
            </h2>
            <p>
              {!address.endsWith(".invalid")
                ? "Your request is handed to the adopter-configured communication system."
                : "Replace the development contact fixture before accepting public requests."}
            </p>
            {!address.endsWith(".invalid") ? (
              <a href={`mailto:${address}`}>{address}</a>
            ) : null}
          </article>
        ))}
      </section>
    </>
  );
}

function SecurityPage() {
  return (
    <>
      <PageIntro
        eyebrow="Security"
        title="Verified boundaries, not certification language."
        description="This summary describes repository-enforced properties only. It does not claim a compliance framework, audit, or certification."
      />
      <section className="shell narrow-shell section prose">
        <h2>Public service boundary</h2>
        <ul>
          <li>The marketing service has no database, vault, or object-storage dependency.</li>
          <li>Its runtime configuration rejects credential-bearing URLs.</li>
          <li>Authentication handoff uses a configured product origin and fixed paths.</li>
          <li>Optional analytics is disabled and cannot block rendering.</li>
        </ul>
        <h2>Operational transparency</h2>
        <p>
          Current availability belongs on the externally hosted{" "}
          <a href={siteConfig.urls.status}>status site</a>. Report a security
          concern through the configured contact on the <Link href="/contact">contact page</Link>.
        </p>
      </section>
    </>
  );
}

function ConsentPage() {
  return (
    <>
      <PageIntro
        eyebrow="Privacy choices"
        title="Consent preferences"
        description="The default public site loads no analytics, advertising, replay, or chat adapter."
      />
      <section className="shell narrow-shell section prose">
        <h2>Current configuration</h2>
        <p>
          Analytics adapter: <strong>{siteConfig.analytics.adapter}</strong>.
          Optional adapters must remain disabled until the adopter connects the
          versioned consent contract in the product.
        </p>
        <p>
          Authenticated legal acceptance is recorded by the product, not by a
          public browser-storage substitute.
        </p>
      </section>
    </>
  );
}

function AccessibilityPage() {
  return (
    <>
      <PageIntro
        eyebrow="Accessibility"
        title="Accessibility statement template"
        description="This starter targets WCAG 2.2 AA practices, but automated checks are not certification."
      />
      <section className="shell narrow-shell section prose">
        <h2>Included foundation</h2>
        <p>
          Pages use semantic landmarks, a skip link, ordered headings, visible
          focus, reduced-motion styles, responsive layout, and labeled code
          examples. Adopters must manually audit their final content, colors,
          integrations, and critical journeys.
        </p>
        <h2>Feedback</h2>
        <p>
          Configure a real support address before launch so visitors can report
          an accessibility barrier.
        </p>
      </section>
    </>
  );
}

function MaintenancePage() {
  return (
    <PageIntro
      eyebrow="Maintenance"
      title="The public site is in a planned maintenance state."
      description="Product and incident details belong on the external status system. Cached public information remains available through the navigation."
    >
      <div className="button-row">
        <a className="button" href={siteConfig.urls.status}>
          View service status
        </a>
        <Link className="button button-quiet" href="/docs">
          Browse documentation
        </Link>
      </div>
    </PageIntro>
  );
}

function PlanCard({
  plan,
  attribution,
}: {
  plan: PublicPlan;
  attribution: AttributionInput;
}) {
  const handoff = plan.contactSales
    ? !siteConfig.contacts.sales.endsWith(".invalid")
      ? `mailto:${siteConfig.contacts.sales}?subject=${encodeURIComponent(`${plan.name} plan`)}`
      : null
    : plan.checkoutEnabled
      ? productHandoff("signup", attribution, plan.key)
      : null;
  return (
    <article className="price-card">
      <div className="price-card-heading">
        <div>
          <h2>{plan.name}</h2>
          {plan.fixture ? <span className="tag">Development fixture</span> : null}
        </div>
        <p className="price">
          {formatPlanAmount(plan, siteConfig.locales.default)}
          {plan.amountMinor !== null &&
          plan.amountMinor > 0 &&
          plan.interval !== "contact" ? (
            <small> / {plan.interval}</small>
          ) : null}
        </p>
      </div>
      <p>{plan.description}</p>
      <dl className="plan-terms">
        <div>
          <dt>Trial</dt>
          <dd>{plan.trialDays > 0 ? `${plan.trialDays} days` : "None"}</dd>
        </div>
        <div>
          <dt>Tax</dt>
          <dd>{plan.taxBehavior}</dd>
        </div>
      </dl>
      <ul>
        {plan.entitlements.map((entitlement) => (
          <li key={entitlement.key}>
            {entitlement.key.replaceAll("_", " ")}:{" "}
            {entitlement.limit < 0 ? "unlimited" : entitlement.limit}
          </li>
        ))}
      </ul>
      {handoff ? (
        <a className="button" href={handoff}>
          {plan.contactSales ? "Contact sales" : "Select plan"}
        </a>
      ) : (
        <span className="button button-disabled" aria-disabled="true">
          Currently unavailable
        </span>
      )}
    </article>
  );
}

async function PricingPage({ searchParams }: { searchParams: AttributionInput }) {
  const state = await loadPublicPlans();
  return (
    <>
      <PageIntro
        eyebrow="Pricing"
        title="Plans from the checkout catalog."
        description="Price, currency, interval, trial, tax behavior, availability, and entitlements come from one sanitized authoritative projection."
      />
      <section className="shell section">
        {state.kind === "available" ? (
          <>
            <p className="catalog-revision">Catalog revision {state.catalog.revision}</p>
            <div className="pricing-grid">
              {state.catalog.plans.map((plan) => (
                <PlanCard
                  attribution={searchParams}
                  key={plan.key}
                  plan={plan}
                />
              ))}
            </div>
          </>
        ) : (
          <EmptyState
            title={
              state.kind === "disabled"
                ? "Public pricing is not configured"
                : "Pricing is temporarily unavailable"
            }
            description={
              state.kind === "disabled"
                ? "Configure the public catalog endpoint to publish authoritative plans."
                : "The product catalog did not respond. Public content remains available and no price is guessed from stale marketing copy."
            }
          />
        )}
      </section>
    </>
  );
}

async function DocsSearch({ query }: { query: string }) {
  const results = await repositoryContentProvider.searchDocs(query);
  return (
    <>
      <PageIntro
        eyebrow="Documentation search"
        title="Search the launch documentation."
        description="The default provider uses a deterministic local index. An external search adapter can replace it without changing document rendering."
      >
        <form action="/docs/search" className="search-form" role="search">
          <label htmlFor="docs-query">Search documentation</label>
          <div>
            <input
              defaultValue={query}
              id="docs-query"
              maxLength={100}
              name="q"
              placeholder="Try “deployment”"
              type="search"
            />
            <button className="button" type="submit">
              Search
            </button>
          </div>
        </form>
      </PageIntro>
      <section className="shell section">
        {query ? (
          <ContentGrid documents={results} prefix="/docs" />
        ) : (
          <EmptyState
            title="Enter a search term"
            description="Search runs only after you submit a query."
          />
        )}
      </section>
    </>
  );
}

function firstParam(value: string | string[] | undefined): string {
  return (Array.isArray(value) ? value[0] : value)?.trim().slice(0, 100) ?? "";
}

export async function renderMarketingPage({
  segments,
  searchParams,
}: MarketingRoute): Promise<ReactNode> {
  const route = `/${segments.join("/")}`;
  if (route === "/") return <Home searchParams={searchParams} />;
  if (route === "/product") return <ProductPage />;
  if (route === "/pricing") return <PricingPage searchParams={searchParams} />;
  if (route === "/about") return <AboutPage />;
  if (route === "/contact") return <ContactPage />;
  if (route === "/security") return <SecurityPage />;
  if (route === "/consent") return <ConsentPage />;
  if (route === "/accessibility") return <AccessibilityPage />;
  if (route === "/maintenance") return <MaintenancePage />;

  if (route === "/use-cases") {
    return (
      <ContentIndex
        kind="use-case"
        eyebrow="Use cases"
        title="Adapt the shell to the product you are building."
        description="These examples describe starter integration patterns, not customer results."
        prefix="/use-cases"
      />
    );
  }
  if (segments[0] === "use-cases" && segments.length === 2) {
    return <ContentDetail kind="use-case" slug={segments[1]} />;
  }
  if (route === "/blog") {
    return (
      <ContentIndex
        kind="article"
        eyebrow="Blog"
        title="Notes for building and launching."
        description="Repository-backed articles carry explicit author, reviewer, publication, and revision metadata."
        prefix="/blog"
      />
    );
  }
  if (segments[0] === "blog" && segments[1] === "tag" && segments.length === 3) {
    return (
      <ContentIndex
        kind="article"
        eyebrow="Blog tag"
        title={`Articles tagged “${segments[2]}”`}
        description="Published articles in this tag."
        prefix="/blog"
        filter={(document) => document.tags.includes(segments[2])}
      />
    );
  }
  if (
    segments[0] === "blog" &&
    segments[1] === "author" &&
    segments.length === 3
  ) {
    const author = segments[2].replaceAll("-", " ");
    return (
      <ContentIndex
        kind="article"
        eyebrow="Author"
        title={`Articles by ${author}`}
        description="Published articles reviewed through the repository workflow."
        prefix="/blog"
        filter={(document) => document.author.toLowerCase() === author.toLowerCase()}
      />
    );
  }
  if (segments[0] === "blog" && segments.length === 2) {
    return <ContentDetail kind="article" slug={segments[1]} />;
  }
  if (route === "/docs") {
    return (
      <>
        <PageIntro
          eyebrow="Documentation"
          title="Launch and extraction guides."
          description="Documentation is versioned with the service and searchable through a provider-neutral interface."
        >
          <Link className="button button-quiet" href="/docs/search">
            Search docs
          </Link>
        </PageIntro>
        <section className="shell section">
          <ContentGrid
            documents={await repositoryContentProvider.list("doc")}
            prefix="/docs"
          />
        </section>
      </>
    );
  }
  if (route === "/docs/search") {
    return <DocsSearch query={firstParam(searchParams.q)} />;
  }
  if (segments[0] === "docs" && segments.length === 2) {
    return <ContentDetail kind="doc" slug={segments[1]} />;
  }
  if (route === "/changelog") {
    return (
      <ContentIndex
        kind="changelog"
        eyebrow="Changelog"
        title="Published release notes."
        description="Each entry names its release identifier and affected features without promising notification delivery."
        prefix="/changelog"
      />
    );
  }
  if (segments[0] === "changelog" && segments.length === 2) {
    return <ContentDetail kind="changelog" slug={segments[1]} />;
  }
  if (segments[0] === "legal" && segments.length === 2) {
    return <ContentDetail kind="legal" slug={segments[1]} />;
  }
  notFound();
}

const staticRoutes = [
  [],
  ["about"],
  ["accessibility"],
  ["blog"],
  ["changelog"],
  ["consent"],
  ["contact"],
  ["docs"],
  ["docs", "search"],
  ["maintenance"],
  ["pricing"],
  ["product"],
  ["security"],
  ["use-cases"],
];

const contentPrefixes: Record<ContentKind, string[]> = {
  article: ["blog"],
  changelog: ["changelog"],
  doc: ["docs"],
  legal: ["legal"],
  "use-case": ["use-cases"],
};

export async function marketingRouteInventory(): Promise<string[][]> {
  const routes = [...staticRoutes];
  for (const document of await allPublicContent()) {
    routes.push([...contentPrefixes[document.type], document.slug]);
    if (document.type === "article") {
      for (const tag of document.tags) routes.push(["blog", "tag", tag]);
      routes.push([
        "blog",
        "author",
        document.author.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
      ]);
    }
  }
  const unique = new Map(routes.map((segments) => [segments.join("/"), segments]));
  return [...unique.values()].sort((left, right) =>
    left.join("/").localeCompare(right.join("/")),
  );
}

export async function metadataForRoute(segments: string[]): Promise<{
  title: string;
  description: string;
}> {
  const route = `/${segments.join("/")}`;
  const named: Record<string, [string, string]> = {
    "/": [
      siteConfig.company.productName,
      siteConfig.company.shortDescription,
    ],
    "/product": ["Product", "Public and authenticated product foundations."],
    "/pricing": ["Pricing", "Plans from the authoritative checkout catalog."],
    "/about": ["About", siteConfig.company.longDescription],
    "/contact": ["Contact", "Contact the configured company team."],
    "/blog": ["Blog", "Published launch and product notes."],
    "/docs": ["Documentation", "Launch, deployment, and extraction guides."],
    "/docs/search": ["Search documentation", "Search published documentation."],
    "/changelog": ["Changelog", "Published release notes."],
    "/security": ["Security", "Verified public-service security boundaries."],
    "/consent": ["Consent preferences", "Public analytics and consent state."],
    "/accessibility": [
      "Accessibility",
      "Accessibility foundation and manual audit expectations.",
    ],
    "/maintenance": ["Maintenance", "Planned public-site maintenance state."],
    "/use-cases": ["Use cases", "Starter integration patterns."],
  };
  if (named[route]) return { title: named[route][0], description: named[route][1] };
  if (
    segments[0] === "blog" &&
    segments[1] === "tag" &&
    segments.length === 3
  ) {
    return {
      title: `Articles tagged ${segments[2]}`,
      description: `Published articles tagged ${segments[2]}.`,
    };
  }
  if (
    segments[0] === "blog" &&
    segments[1] === "author" &&
    segments.length === 3
  ) {
    const author = segments[2].replaceAll("-", " ");
    return {
      title: `Articles by ${author}`,
      description: `Published articles by ${author}.`,
    };
  }
  const mapping: Record<string, ContentKind> = {
    blog: "article",
    changelog: "changelog",
    docs: "doc",
    legal: "legal",
    "use-cases": "use-case",
  };
  if (segments.length === 2 && mapping[segments[0]]) {
    const document = await repositoryContentProvider.get(
      mapping[segments[0]],
      segments[1],
    );
    if (document) return { title: document.title, description: document.description };
  }
  return {
    title: "Page not found",
    description: "The requested public page was not found.",
  };
}
