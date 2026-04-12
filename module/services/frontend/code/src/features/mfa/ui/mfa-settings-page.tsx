"use client";

import { useCallback, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldCheck, Plus } from "lucide-react";
import { toast } from "sonner";
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/ui";
import { mfaQueries } from "../service/queries";
import { mfaMutations } from "../service/mutations";
import type { MFADevice } from "../model/types";
import { MFADevices } from "./mfa-devices";
import { MFASetup } from "./mfa-setup";

export function MFASettingsPage() {
  const queryClient = useQueryClient();
  const [showSetup, setShowSetup] = useState(false);

  const { data, isLoading } = useQuery(mfaQueries.devices());
  const devices: MFADevice[] =
    (data as { devices?: MFADevice[] } | undefined)?.devices ?? [];

  const revokeMutation = useMutation({
    mutationFn: (deviceId: string) => mfaMutations.revokeDevice(deviceId),
    onSuccess: () => {
      toast.success("Device revoked");
      queryClient.invalidateQueries({ queryKey: ["mfa"] });
    },
    onError: () => toast.error("Failed to revoke device"),
  });

  const handleRevoke = useCallback(
    (device: MFADevice) => revokeMutation.mutate(device.id),
    [revokeMutation],
  );

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          Two-Factor Authentication
        </h2>
        <p className="text-muted-foreground">
          Add an extra layer of security to your account.
        </p>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <div className="flex items-center gap-3">
            <ShieldCheck className="h-5 w-5 text-primary" />
            <div>
              <CardTitle className="text-lg">MFA Devices</CardTitle>
              <CardDescription>
                {devices.length > 0
                  ? `${devices.length} device${devices.length !== 1 ? "s" : ""} enrolled`
                  : "No devices enrolled. Enable 2FA to secure your account."}
              </CardDescription>
            </div>
          </div>
          <Button onClick={() => setShowSetup(true)}>
            <Plus className="mr-2 h-4 w-4" />
            {devices.length > 0 ? "Add Device" : "Enable 2FA"}
          </Button>
        </CardHeader>
        <CardContent>
          <MFADevices
            data={devices}
            isLoading={isLoading}
            onRevoke={handleRevoke}
          />
        </CardContent>
      </Card>

      {showSetup && (
        <MFASetup open onClose={() => setShowSetup(false)} />
      )}
    </div>
  );
}
