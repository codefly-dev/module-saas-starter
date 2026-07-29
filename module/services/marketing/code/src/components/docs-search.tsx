"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

export type SearchDocument = {
  slug: string;
  title: string;
  description: string;
  body: string;
  locale: string;
};

export function DocsSearchResults({
  documents,
  routePrefix,
}: {
  documents: SearchDocument[];
  routePrefix: string;
}) {
  const query = useSearchParams().get("q")?.trim().slice(0, 100) ?? "";
  const terms = query
    .toLocaleLowerCase(documents[0]?.locale)
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 8);
  const results =
    terms.length === 0
      ? []
      : documents.filter((document) => {
          const searchable =
            `${document.title} ${document.description} ${document.body}`.toLocaleLowerCase(
              document.locale,
            );
          return terms.every((term) => searchable.includes(term));
        });
  return (
    <>
      <header className="page-intro shell narrow-shell">
        <p className="eyebrow">Documentation search</p>
        <h1>Search the launch documentation.</h1>
        <p className="lede">
          The default provider uses a deterministic local index. An external
          search adapter can replace it without changing document rendering.
        </p>
        <form
          action={`${routePrefix}/docs/search`}
          className="search-form"
          role="search"
        >
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
      </header>
      <section className="shell section">
        {query ? (
          results.length > 0 ? (
            <div className="content-grid">
              {results.map((document) => (
                <article className="content-card" key={document.slug}>
                  <h2>
                    <Link href={`${routePrefix}/docs/${document.slug}`}>
                      {document.title}
                    </Link>
                  </h2>
                  <p>{document.description}</p>
                  <span className="text-link" aria-hidden="true">
                    Read more →
                  </span>
                </article>
              ))}
            </div>
          ) : (
            <div className="empty-state" role="status">
              <h2>Nothing found</h2>
              <p>Try a different search term.</p>
            </div>
          )
        ) : (
          <div className="empty-state" role="status">
            <h2>Enter a search term</h2>
            <p>Search runs only after you submit a query.</p>
          </div>
        )}
      </section>
    </>
  );
}
