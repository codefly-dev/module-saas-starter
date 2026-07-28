import { Suspense } from "react";
import { LoginPage } from "@/features/auth/ui/login-page";

export default function Page() {
	return (
		<Suspense
			fallback={
				<p className="min-h-screen flex items-center justify-center">
					Loading sign in…
				</p>
			}
		>
			<LoginPage />
		</Suspense>
	);
}
