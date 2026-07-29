import Link from "next/link";
import { PageIntro } from "@/components/site-shell";

export default function NotFound() {
  return (
    <PageIntro
      eyebrow="404"
      title="That page is not published."
      description="The address may have changed, the content may still be a draft, or the link may be incorrect."
    >
      <Link className="button" href="/">
        Return home
      </Link>
    </PageIntro>
  );
}
