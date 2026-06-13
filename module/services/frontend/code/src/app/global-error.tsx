"use client";

// global-error.tsx — App Router root error boundary. Required by
// Next 13+ to catch errors thrown by the root layout / providers
// (errors below that point are caught by per-route error.tsx files,
// but errors IN the root tree need this top-level boundary).
//
// We forward the error to Sentry via captureException so the same
// thing the user sees in their browser appears in the Issues feed
// with a source-mapped stack trace.

import * as Sentry from "@sentry/nextjs";
import NextError from "next/error";
import { useEffect } from "react";

export default function GlobalError({
  error,
}: {
  error: Error & { digest?: string };
}) {
  useEffect(() => {
    Sentry.captureException(error);
  }, [error]);

  return (
    <html lang="en">
      <body>
        {/* NextError renders Next's default 500 page so users still
            get something coherent; the actual signal goes to
            Sentry above. */}
        <NextError statusCode={0} />
      </body>
    </html>
  );
}
