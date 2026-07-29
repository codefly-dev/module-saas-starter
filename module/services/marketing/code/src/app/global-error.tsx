"use client";

export default function GlobalError({
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <html lang="en">
      <body>
        <main className="page-intro shell narrow-shell">
          <p className="eyebrow">Unexpected error</p>
          <h1>The public page could not be rendered.</h1>
          <p className="lede">
            Cached content and the authenticated product are independent of this
            request.
          </p>
          <button className="button" onClick={reset} type="button">
            Try again
          </button>
        </main>
      </body>
    </html>
  );
}
