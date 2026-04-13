"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Copy, Check } from "lucide-react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Button,
  Input,
  Label,
} from "@/shared/ui";
import { verifyTOTPSchema, type VerifyTOTPValues } from "../model/schemas";
import { mfaMutations } from "../service/mutations";

interface MFASetupProps {
  open: boolean;
  onClose: () => void;
}

type SetupStep = "qr" | "verify" | "backup";

export function MFASetup({ open, onClose }: MFASetupProps) {
  const queryClient = useQueryClient();
  const [step, setStep] = useState<SetupStep>("qr");
  const [provisioningUri, setProvisioningUri] = useState<string | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  const [copied, setCopied] = useState(false);

  const form = useForm<VerifyTOTPValues>({
    resolver: zodResolver(verifyTOTPSchema),
    defaultValues: { code: "" },
  });

  const setupMutation = useMutation({
    mutationFn: () => mfaMutations.setupTOTP(),
    onSuccess: (data) => {
      setProvisioningUri(data.provisioningUri);
      setSecret(data.secret);
    },
    onError: () => toast.error("Failed to initialize MFA setup"),
  });

  const verifyMutation = useMutation({
    mutationFn: (code: string) => mfaMutations.verifyTOTP(code),
    onSuccess: async () => {
      toast.success("MFA enabled successfully");
      const result = await mfaMutations.generateBackupCodes();
      setBackupCodes(result.backupCodes);
      setStep("backup");
      queryClient.invalidateQueries({ queryKey: ["mfa"] });
    },
    onError: () => toast.error("Invalid code. Please try again."),
  });

  const handleOpen = (isOpen: boolean) => {
    if (!isOpen) {
      onClose();
      // Reset state on close
      setStep("qr");
      setProvisioningUri(null);
      setSecret(null);
      setBackupCodes([]);
      form.reset();
    }
  };

  // Start setup when dialog opens
  if (open && !provisioningUri && !setupMutation.isPending) {
    setupMutation.mutate();
  }

  const copySecret = () => {
    if (secret) {
      navigator.clipboard.writeText(secret);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpen}>
      <DialogContent className="sm:max-w-[425px]">
        {step === "qr" && (
          <>
            <DialogHeader>
              <DialogTitle>Set Up Two-Factor Authentication</DialogTitle>
              <DialogDescription>
                Scan the QR code with your authenticator app (Google
                Authenticator, Authy, 1Password, etc.).
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4">
              {provisioningUri ? (
                <>
                  <div className="flex justify-center rounded-lg border bg-white p-4">
                    {/* QR code rendered via provisioning URI */}
                    <div className="flex h-48 w-48 items-center justify-center border-2 border-dashed text-xs text-muted-foreground">
                      QR Code
                      <br />
                      {/* In production: use a QR library like qrcode.react */}
                    </div>
                  </div>
                  {secret && (
                    <div className="space-y-1">
                      <Label>Or enter this code manually:</Label>
                      <div className="flex items-center gap-2">
                        <code className="flex-1 rounded bg-muted px-3 py-2 font-mono text-sm tracking-widest">
                          {secret}
                        </code>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={copySecret}
                        >
                          {copied ? (
                            <Check className="h-4 w-4" />
                          ) : (
                            <Copy className="h-4 w-4" />
                          )}
                        </Button>
                      </div>
                    </div>
                  )}
                </>
              ) : (
                <div className="flex h-48 items-center justify-center">
                  <p className="text-sm text-muted-foreground">Loading...</p>
                </div>
              )}
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button onClick={() => setStep("verify")} disabled={!secret}>
                Next
              </Button>
            </DialogFooter>
          </>
        )}

        {step === "verify" && (
          <>
            <DialogHeader>
              <DialogTitle>Verify Your Code</DialogTitle>
              <DialogDescription>
                Enter the 6-digit code from your authenticator app to confirm
                setup.
              </DialogDescription>
            </DialogHeader>
            <form
              onSubmit={form.handleSubmit((vals) =>
                verifyMutation.mutate(vals.code),
              )}
              className="space-y-4"
            >
              <div className="space-y-2">
                <Label htmlFor="totp-code">Verification Code</Label>
                <Input
                  id="totp-code"
                  placeholder="000000"
                  maxLength={6}
                  className="text-center font-mono text-lg tracking-[0.5em]"
                  {...form.register("code")}
                  autoFocus
                />
                {form.formState.errors.code && (
                  <p className="text-sm text-destructive">
                    {form.formState.errors.code.message}
                  </p>
                )}
              </div>
              <DialogFooter>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => setStep("qr")}
                >
                  Back
                </Button>
                <Button type="submit" disabled={verifyMutation.isPending}>
                  {verifyMutation.isPending ? "Verifying..." : "Verify"}
                </Button>
              </DialogFooter>
            </form>
          </>
        )}

        {step === "backup" && (
          <>
            <DialogHeader>
              <DialogTitle>Save Your Backup Codes</DialogTitle>
              <DialogDescription>
                Store these codes in a safe place. Each code can be used once if
                you lose access to your authenticator.
              </DialogDescription>
            </DialogHeader>
            <div className="grid grid-cols-2 gap-2 rounded-lg border bg-muted/50 p-4">
              {backupCodes.map((code) => (
                <code
                  key={code}
                  className="rounded bg-background px-2 py-1 text-center font-mono text-sm"
                >
                  {code}
                </code>
              ))}
            </div>
            <DialogFooter>
              <Button
                variant="outline"
                onClick={() => {
                  navigator.clipboard.writeText(backupCodes.join("\n"));
                  toast.success("Backup codes copied to clipboard");
                }}
              >
                <Copy className="mr-2 h-4 w-4" />
                Copy All
              </Button>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
