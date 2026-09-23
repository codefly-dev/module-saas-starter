import { Suspense } from "react";
import { LoginPage } from "@/features/auth/ui/login-page";
import { readIdentityConfig } from "@/lib/identity-config";

export default async function Page() {
	const identity = await readIdentityConfig();
	return (
		<Suspense
			fallback={
				<p className="min-h-screen flex items-center justify-center">
					Loading sign in…
				</p>
			}
		>
			<LoginPage identity={identity} />
		</Suspense>
	);
}
