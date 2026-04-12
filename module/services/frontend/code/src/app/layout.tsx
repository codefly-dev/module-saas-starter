import type { Metadata } from "next";
import { Providers } from "@/lib/providers";
import { AdminLayout } from "@/components/admin-layout";
import { ImpersonationBanner } from "@/components/impersonation-banner";
import { OnboardingGate } from "@/features/onboarding/ui/onboarding-gate";
import "./globals.css";
import { Geist } from "next/font/google";
import { cn } from "@/lib/utils";

const geist = Geist({subsets:['latin'],variable:'--font-sans'});

export const metadata: Metadata = {
  title: "Admin Dashboard",
  description: "User management administration",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" suppressHydrationWarning className={cn("font-sans", geist.variable)}>
      <body className="bg-gray-50 dark:bg-gray-950 text-gray-900 dark:text-gray-100 antialiased">
        <Providers>
          <ImpersonationBanner />
          <OnboardingGate>
            <AdminLayout>{children}</AdminLayout>
          </OnboardingGate>
        </Providers>
      </body>
    </html>
  );
}
