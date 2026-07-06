"use client";

import { useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Button,
  Label,
  Switch,
} from "@/shared/ui";
import { userSettingsQueries } from "@/features/user-settings/service/queries";
import { userSettingsMutations } from "@/features/user-settings/service/mutations";

interface Channel {
  id: "inApp" | "push" | "sound";
  label: string;
  description: string;
}

/** The delivery channels the backend actually persists (UserNotificationSettings:
 * in_app / push / sound). Per-event granularity would need a proto extension; these
 * are the global toggles the api stores today. */
const CHANNELS: Channel[] = [
  { id: "inApp", label: "In-app", description: "Show notifications in the app's notification center." },
  { id: "push", label: "Push", description: "Send push notifications to your registered devices." },
  { id: "sound", label: "Sound", description: "Play a sound when a notification arrives." },
];

type Prefs = { inApp: boolean; push: boolean; sound: boolean };

/**
 * Notification preferences — wired to the real UserSettings.notifications
 * (in_app / push / sound). Reads the current settings and persists changes via
 * UserSettingsService.Update (the nested `notifications` object is replaced
 * wholesale, so we always send all three).
 */
export function NotificationSettings() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useQuery(userSettingsQueries.current());

  const [prefs, setPrefs] = useState<Prefs>({ inApp: true, push: false, sound: false });

  // Seed local state from the server settings once they load.
  useEffect(() => {
    const n = data?.notifications;
    if (n) {
      setPrefs({ inApp: n.inApp ?? true, push: n.push ?? false, sound: n.sound ?? false });
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () =>
      userSettingsMutations.update({
        notifications: { inApp: prefs.inApp, push: prefs.push, sound: prefs.sound },
      }),
    onSuccess: () => {
      toast.success("Notification preferences saved");
      queryClient.invalidateQueries({ queryKey: ["user-settings"] });
    },
    onError: () => toast.error("Failed to save preferences"),
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Notification Preferences</h1>
        <p className="text-muted-foreground">Choose how you want to be notified.</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Delivery channels</CardTitle>
          <CardDescription>These apply to the notifications this account receives.</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="divide-y">
            {CHANNELS.map((ch) => (
              <div key={ch.id} className="flex items-center justify-between gap-4 py-4 first:pt-0 last:pb-0">
                <div className="space-y-0.5">
                  <Label htmlFor={`notif-${ch.id}`} className="text-sm font-medium">
                    {ch.label}
                  </Label>
                  <p className="text-sm text-muted-foreground">{ch.description}</p>
                </div>
                <Switch
                  id={`notif-${ch.id}`}
                  checked={prefs[ch.id]}
                  disabled={isLoading}
                  onCheckedChange={(v) => setPrefs((p) => ({ ...p, [ch.id]: v }))}
                />
              </div>
            ))}
          </div>
        </CardContent>
        <CardFooter>
          <Button onClick={() => save.mutate()} disabled={isLoading || save.isPending}>
            {save.isPending ? "Saving..." : "Save Preferences"}
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
