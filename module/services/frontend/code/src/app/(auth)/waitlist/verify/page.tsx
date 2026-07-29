import type { Metadata } from "next";
import { WaitlistVerification } from "@/features/waitlist/ui/waitlist-verification";

export const metadata: Metadata = {
	title: "Verify waitlist email",
	referrer: "no-referrer",
};

export default function Page() {
	return <WaitlistVerification />;
}
