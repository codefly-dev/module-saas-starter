import Link from "next/link";
import { Suspense, type ReactNode } from "react";
import { AttributionAcquisitionActions } from "@/components/attribution-handoff";
import { siteConfig } from "@/config/site";
import {
  primaryAcquisitionHandoff,
  productHandoff,
} from "@/lib/cta";

const navigation = [
  { href: "/product", label: "Product" },
  { href: "/use-cases", label: "Use cases" },
  { href: "/pricing", label: "Pricing" },
  { href: "/docs", label: "Docs" },
  { href: "/blog", label: "Blog" },
];

export function Brand() {
  return (
    <Link className="brand" href="/" aria-label={`${siteConfig.company.productName} home`}>
      <span className="brand-mark" aria-hidden="true">
        {siteConfig.brand.mark}
      </span>
      <span>{siteConfig.company.productName}</span>
    </Link>
  );
}

export function SiteHeader() {
  const login = productHandoff("login");
  return (
    <header className="site-header">
      <div className="shell nav-shell">
        <Brand />
        <nav aria-label="Primary navigation">
          <ul className="primary-nav">
            {navigation.map((item) => (
              <li key={item.href}>
                <Link href={item.href}>{item.label}</Link>
              </li>
            ))}
          </ul>
        </nav>
        {login ? (
          <a className="button button-small button-quiet" href={login}>
            Sign in
          </a>
        ) : null}
      </div>
    </header>
  );
}

export function SiteFooter() {
  return (
    <footer className="site-footer">
      <div className="shell footer-grid">
        <div>
          <Brand />
          <p className="footer-description">{siteConfig.company.shortDescription}</p>
          {siteConfig.developmentFixture ? (
            <p className="fixture-note">Development fixture — replace before launch.</p>
          ) : null}
        </div>
        <nav aria-label="Company">
          <h2>Company</h2>
          <ul>
            <li>
              <Link href="/about">About</Link>
            </li>
            <li>
              <Link href="/contact">Contact</Link>
            </li>
            <li>
              <Link href="/changelog">Changelog</Link>
            </li>
            <li>
              <a href={siteConfig.urls.status}>Status</a>
            </li>
          </ul>
        </nav>
        <nav aria-label="Trust and legal">
          <h2>Trust &amp; legal</h2>
          <ul>
            <li>
              <Link href="/security">Security</Link>
            </li>
            <li>
              <Link href="/legal/privacy">Privacy</Link>
            </li>
            <li>
              <Link href="/legal/terms">Terms</Link>
            </li>
            <li>
              <Link href="/consent">Consent preferences</Link>
            </li>
            <li>
              <Link href="/accessibility">Accessibility</Link>
            </li>
          </ul>
        </nav>
      </div>
      <div className="shell footer-bottom">
        <span>
          © {new Date().getUTCFullYear()} {siteConfig.company.name}
        </span>
        <span>No customer, compliance, or performance claims are included.</span>
      </div>
    </footer>
  );
}

export function PageIntro({
  eyebrow,
  title,
  description,
  children,
}: {
  eyebrow: string;
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <header className="page-intro shell narrow-shell">
      <p className="eyebrow">{eyebrow}</p>
      <h1>{title}</h1>
      <p className="lede">{description}</p>
      {children}
    </header>
  );
}

export function AcquisitionCTA({
  heading = "Move from starter to your product",
}: {
  heading?: string;
}) {
  return (
    <section className="shell callout" aria-labelledby="acquisition-heading">
      <div>
        <p className="eyebrow">Your next step</p>
        <h2 id="acquisition-heading">{heading}</h2>
        <p>
          The public site hands approved campaign context to the product without
          accepting arbitrary redirect destinations.
        </p>
      </div>
      <Suspense fallback={<AcquisitionActionsFallback />}>
        <AttributionAcquisitionActions />
      </Suspense>
    </section>
  );
}

function AcquisitionActionsFallback() {
  const handoff = primaryAcquisitionHandoff();
  const login = productHandoff("login");
  return (
    <div className="button-row">
      {handoff.available && handoff.href ? (
        <a className="button" href={handoff.href}>
          {handoff.label}
        </a>
      ) : (
        <span className="button button-disabled" aria-disabled="true">
          {handoff.label} unavailable
        </span>
      )}
      {login && handoff.href !== login ? (
        <a className="button button-quiet" href={login}>
          Sign in
        </a>
      ) : null}
    </div>
  );
}

export function EmptyState({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="empty-state" role="status">
      <h2>{title}</h2>
      <p>{description}</p>
    </div>
  );
}
