import { Suspense } from "react";
import { LoginPage } from "@/features/auth/ui/login-page";
export default function Page() {
	return (
		<Suspense>
			<LoginPage />
		</Suspense>
	);
}
