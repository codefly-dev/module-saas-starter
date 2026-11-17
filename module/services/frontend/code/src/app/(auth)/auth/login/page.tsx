import { Suspense } from "react";
import { LoginPage } from "@/features/auth/ui/login-page";
import { readIdentityConfig } from "@/lib/identity-config";
import { Card, CardContent, CardHeader, Skeleton } from "@/shared/ui";

export default async function Page() {
	const identity = await readIdentityConfig();
	return (
		<Suspense
			fallback={
				<div className="flex min-h-screen w-full items-center justify-center p-4">
					<Card className="w-full max-w-md">
						<CardHeader>
							<Skeleton className="h-7 w-32" />
							<Skeleton className="h-4 w-full" />
						</CardHeader>
						<CardContent>
							<Skeleton className="h-11 w-full" />
						</CardContent>
					</Card>
				</div>
			}
		>
			<LoginPage identity={identity} />
		</Suspense>
	);
}
