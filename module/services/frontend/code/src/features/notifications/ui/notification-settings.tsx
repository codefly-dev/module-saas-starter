"use client";

import { useState } from "react";
import { toast } from "sonner";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Button,
  Checkbox,
  Label,
} from "@/shared/ui";

interface NotificationChannel {
  id: string;
  label: string;
}

interface EventPreference {
  eventType: string;
  label: string;
  channels: Record<string, boolean>;
}

const CHANNELS: NotificationChannel[] = [
  { id: "in_app", label: "In-App" },
  { id: "email", label: "Email" },
  { id: "slack", label: "Slack" },
];

const DEFAULT_EVENTS: EventPreference[] = [
  {
    eventType: "user.registered",
    label: "New user registration",
    channels: { in_app: true, email: true, slack: false },
  },
  {
    eventType: "auth.login",
    label: "User login",
    channels: { in_app: false, email: false, slack: false },
  },
  {
    eventType: "invitation.created",
    label: "Invitation sent",
    channels: { in_app: true, email: true, slack: false },
  },
  {
    eventType: "invitation.accepted",
    label: "Invitation accepted",
    channels: { in_app: true, email: false, slack: false },
  },
  {
    eventType: "org.member_added",
    label: "Member added to org",
    channels: { in_app: true, email: false, slack: true },
  },
  {
    eventType: "org.member_removed",
    label: "Member removed from org",
    channels: { in_app: true, email: true, slack: true },
  },
  {
    eventType: "api_key.created",
    label: "API key created",
    channels: { in_app: true, email: true, slack: false },
  },
  {
    eventType: "role.assigned",
    label: "Role assigned",
    channels: { in_app: true, email: false, slack: false },
  },
  {
    eventType: "platform.user_impersonated",
    label: "User impersonation started",
    channels: { in_app: true, email: true, slack: true },
  },
];

/**
 * Notification preferences scaffold.
 * Backend notification preferences API can be wired in later; for now
 * this is a UI-only component with local state.
 */
export function NotificationSettings() {
  const [events, setEvents] = useState<EventPreference[]>(DEFAULT_EVENTS);

  const toggleChannel = (eventType: string, channelId: string) => {
    setEvents((prev) =>
      prev.map((e) =>
        e.eventType === eventType
          ? {
              ...e,
              channels: {
                ...e.channels,
                [channelId]: !e.channels[channelId],
              },
            }
          : e,
      ),
    );
  };

  const handleSave = () => {
    // Placeholder: will call backend when preferences API exists
    toast.success("Notification preferences saved");
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">
          Notification Preferences
        </h1>
        <p className="text-muted-foreground">
          Choose how you want to be notified for each event type.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Event Notifications</CardTitle>
          <CardDescription>
            Toggle notification channels per event type.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="space-y-1">
            {/* Header */}
            <div className="grid grid-cols-[1fr_repeat(3,80px)] items-center gap-2 border-b pb-2 text-sm font-medium text-muted-foreground">
              <span>Event</span>
              {CHANNELS.map((ch) => (
                <span key={ch.id} className="text-center">
                  {ch.label}
                </span>
              ))}
            </div>

            {/* Rows */}
            {events.map((event) => (
              <div
                key={event.eventType}
                className="grid grid-cols-[1fr_repeat(3,80px)] items-center gap-2 py-2"
              >
                <Label className="text-sm">{event.label}</Label>
                {CHANNELS.map((ch) => (
                  <div key={ch.id} className="flex justify-center">
                    <Checkbox
                      checked={event.channels[ch.id] ?? false}
                      onCheckedChange={() =>
                        toggleChannel(event.eventType, ch.id)
                      }
                    />
                  </div>
                ))}
              </div>
            ))}
          </div>
        </CardContent>
        <CardFooter>
          <Button onClick={handleSave}>Save Preferences</Button>
        </CardFooter>
      </Card>
    </div>
  );
}
