"use client";

import { useEffect, useState } from "react";
import { useTheme } from "next-themes";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Checkbox,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Skeleton,
} from "@/shared/ui";
import { userSettingsQueries } from "../service/queries";
import { userSettingsMutations } from "../service/mutations";

/**
 * GeneralSettingsPage — the top-level /settings hub. Surfaces the
 * preferences blob (theme / locale / timezone / formats / email
 * opt-ins) in three cards, each with an inline Save so the operator
 * can update one section without committing the others.
 *
 * Theme syncs both ways: applying the theme via next-themes also
 * persists it to the api on save, so a fresh device login restores
 * the user's preference.
 */
export function GeneralSettingsPage() {
  const queryClient = useQueryClient();
  const { setTheme } = useTheme();
  const { data: settings, isLoading } = useQuery(userSettingsQueries.current());

  // Local form state. Initialized from the api on load; saves below
  // submit a partial patch with only the keys this card owns.
  const [theme, setLocalTheme] = useState<string>("system");
  const [locale, setLocale] = useState<string>("en");
  const [timezone, setTimezone] = useState<string>("UTC");
  const [dateFormat, setDateFormat] = useState<string>("iso");
  const [timeFormat, setTimeFormat] = useState<string>("24h");

  const [emailProduct, setEmailProduct] = useState(true);
  const [emailMarketing, setEmailMarketing] = useState(false);
  const [emailWeeklyDigest, setEmailWeeklyDigest] = useState(true);

  useEffect(() => {
    if (!settings) return;
    if (settings.theme) setLocalTheme(settings.theme);
    if (settings.locale) setLocale(settings.locale);
    if (settings.timezone) setTimezone(settings.timezone);
    if (settings.dateFormat) setDateFormat(settings.dateFormat);
    if (settings.timeFormat) setTimeFormat(settings.timeFormat);
    if (settings.email) {
      if (settings.email.product !== undefined)
        setEmailProduct(settings.email.product);
      if (settings.email.marketing !== undefined)
        setEmailMarketing(settings.email.marketing);
      if (settings.email.weeklyDigest !== undefined)
        setEmailWeeklyDigest(settings.email.weeklyDigest);
    }
  }, [settings]);

  const save = useMutation({
    mutationFn: userSettingsMutations.update,
    onSuccess: () => {
      toast.success("Settings saved");
      void queryClient.invalidateQueries({ queryKey: ["user-settings"] });
    },
    onError: (err) =>
      toast.error("Save failed", { description: err.message }),
  });

  function saveAppearance() {
    // Apply locally so the page repaints immediately; the api save
    // persists for next-device.
    setTheme(theme);
    save.mutate({ theme, locale });
  }

  function saveTimezone() {
    save.mutate({ timezone, dateFormat, timeFormat });
  }

  function saveEmail() {
    save.mutate({
      email: {
        $typeName: "customers.UserEmailSettings",
        product: emailProduct,
        marketing: emailMarketing,
        weeklyDigest: emailWeeklyDigest,
        // security forced-on server-side; we don't send it.
      },
    });
  }

  if (isLoading) {
    return (
      <div className="space-y-4 max-w-3xl">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  return (
    <div className="space-y-6 max-w-3xl">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Preferences</h1>
        <p className="text-muted-foreground">
          Theme, locale, and email preferences. Synced across all your devices.
        </p>
      </div>

      {/* ── Appearance ── */}
      <Card>
        <CardHeader>
          <CardTitle>Appearance</CardTitle>
          <CardDescription>
            Theme follows your OS by default. Pick a fixed theme to override.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 gap-4 max-w-md">
            <div className="space-y-2">
              <Label htmlFor="theme">Theme</Label>
              <Select value={theme} onValueChange={(v) => v && setLocalTheme(v)}>
                <SelectTrigger id="theme">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="system">System</SelectItem>
                  <SelectItem value="light">Light</SelectItem>
                  <SelectItem value="dark">Dark</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="locale">Language</Label>
              <Select value={locale} onValueChange={(v) => v && setLocale(v)}>
                <SelectTrigger id="locale">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="en">English</SelectItem>
                  <SelectItem value="es">Español</SelectItem>
                  <SelectItem value="fr">Français</SelectItem>
                  <SelectItem value="de">Deutsch</SelectItem>
                  <SelectItem value="ja">日本語</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="flex justify-end">
            <Button onClick={saveAppearance} disabled={save.isPending}>
              {save.isPending ? "Saving…" : "Save appearance"}
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* ── Time & date ── */}
      <Card>
        <CardHeader>
          <CardTitle>Time & date</CardTitle>
          <CardDescription>
            How dates render across the product. Affects audit log, billing
            cycles, webhook delivery timestamps.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-3 gap-4">
            <div className="space-y-2 col-span-2">
              <Label htmlFor="tz">Time zone (IANA)</Label>
              <Input
                id="tz"
                placeholder="America/New_York"
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="time-format">Time format</Label>
              <Select value={timeFormat} onValueChange={(v) => v && setTimeFormat(v)}>
                <SelectTrigger id="time-format">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="24h">24-hour</SelectItem>
                  <SelectItem value="12h">12-hour</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-2 max-w-xs">
            <Label htmlFor="date-format">Date format</Label>
            <Select value={dateFormat} onValueChange={(v) => v && setDateFormat(v)}>
              <SelectTrigger id="date-format">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="iso">ISO (2026-04-25)</SelectItem>
                <SelectItem value="us">US (04/25/2026)</SelectItem>
                <SelectItem value="eu">EU (25/04/2026)</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex justify-end">
            <Button onClick={saveTimezone} disabled={save.isPending}>
              {save.isPending ? "Saving…" : "Save time & date"}
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* ── Email ── */}
      <Card>
        <CardHeader>
          <CardTitle>Email preferences</CardTitle>
          <CardDescription>
            Macro categories for transactional email. Per-event overrides
            live in <code>/settings/notifications</code>. Security alerts
            are always on.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <label className="flex items-start gap-3 cursor-pointer">
            <Checkbox
              checked={emailProduct}
              onCheckedChange={(v) => setEmailProduct(v === true)}
            />
            <div className="space-y-0.5">
              <div className="font-medium text-sm">Product updates</div>
              <div className="text-xs text-muted-foreground">
                New features, important changes, planned maintenance.
              </div>
            </div>
          </label>
          <label className="flex items-start gap-3 cursor-pointer">
            <Checkbox
              checked={emailWeeklyDigest}
              onCheckedChange={(v) => setEmailWeeklyDigest(v === true)}
            />
            <div className="space-y-0.5">
              <div className="font-medium text-sm">Weekly digest</div>
              <div className="text-xs text-muted-foreground">
                Quiet rollup of activity across your orgs once a week.
              </div>
            </div>
          </label>
          <label className="flex items-start gap-3 cursor-pointer">
            <Checkbox
              checked={emailMarketing}
              onCheckedChange={(v) => setEmailMarketing(v === true)}
            />
            <div className="space-y-0.5">
              <div className="font-medium text-sm">Marketing</div>
              <div className="text-xs text-muted-foreground">
                Newsletters and promotions. Off by default.
              </div>
            </div>
          </label>
          <label className="flex items-start gap-3 opacity-60 cursor-not-allowed">
            <Checkbox checked disabled />
            <div className="space-y-0.5">
              <div className="font-medium text-sm">Security alerts</div>
              <div className="text-xs text-muted-foreground">
                Always on. Sign-in from a new device, password / MFA changes.
              </div>
            </div>
          </label>
          <div className="flex justify-end pt-2">
            <Button onClick={saveEmail} disabled={save.isPending}>
              {save.isPending ? "Saving…" : "Save email preferences"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
