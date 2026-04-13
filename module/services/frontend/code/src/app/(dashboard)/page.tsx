"use client";

import { useAuth } from "@/lib/auth";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Shield, Bell, FileText } from "lucide-react";
import Link from "next/link";

export default function DashboardPage() {
  const { user } = useAuth();

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Welcome back</h1>
        <p className="text-muted-foreground mt-1">
          Here&apos;s an overview of your account.
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <Link href="/settings/mfa">
          <Card className="hover:border-primary/50 transition-colors cursor-pointer">
            <CardHeader className="flex flex-row items-center gap-3 pb-2">
              <Shield className="h-5 w-5 text-muted-foreground" />
              <CardTitle className="text-sm font-medium">Security</CardTitle>
            </CardHeader>
            <CardContent>
              <CardDescription>Manage MFA and security settings</CardDescription>
            </CardContent>
          </Card>
        </Link>

        <Link href="/settings/notifications">
          <Card className="hover:border-primary/50 transition-colors cursor-pointer">
            <CardHeader className="flex flex-row items-center gap-3 pb-2">
              <Bell className="h-5 w-5 text-muted-foreground" />
              <CardTitle className="text-sm font-medium">Notifications</CardTitle>
            </CardHeader>
            <CardContent>
              <CardDescription>Configure notification preferences</CardDescription>
            </CardContent>
          </Card>
        </Link>

        <Link href="/settings/data">
          <Card className="hover:border-primary/50 transition-colors cursor-pointer">
            <CardHeader className="flex flex-row items-center gap-3 pb-2">
              <FileText className="h-5 w-5 text-muted-foreground" />
              <CardTitle className="text-sm font-medium">Data & Privacy</CardTitle>
            </CardHeader>
            <CardContent>
              <CardDescription>Export data or manage your account</CardDescription>
            </CardContent>
          </Card>
        </Link>
      </div>
    </div>
  );
}
