"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import {
  Button,
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Input,
  Label,
} from "@/shared/ui";
import { verifyTOTPSchema, type VerifyTOTPValues } from "../model/schemas";
import { mfaMutations } from "../service/mutations";

interface MFAChallengeProps {
  onVerified: () => void;
  onCancel?: () => void;
}

export function MFAChallenge({ onVerified, onCancel }: MFAChallengeProps) {
  const [useBackupCode, setUseBackupCode] = useState(false);

  const form = useForm<VerifyTOTPValues>({
    resolver: zodResolver(verifyTOTPSchema),
    defaultValues: { code: "" },
  });

  const verifyMutation = useMutation({
    mutationFn: (code: string) => mfaMutations.verifyTOTP(code),
    onSuccess: (data) => {
      if (data.valid) {
        onVerified();
      } else {
        toast.error("Invalid code. Please try again.");
      }
    },
    onError: () => toast.error("Verification failed. Please try again."),
  });

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-primary/10">
            <ShieldCheck className="h-6 w-6 text-primary" />
          </div>
          <CardTitle>Two-Factor Authentication</CardTitle>
          <CardDescription>
            {useBackupCode
              ? "Enter one of your backup codes"
              : "Enter the 6-digit code from your authenticator app"}
          </CardDescription>
        </CardHeader>

        <form
          onSubmit={form.handleSubmit((vals) =>
            verifyMutation.mutate(vals.code),
          )}
        >
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="mfa-code">
                {useBackupCode ? "Backup Code" : "Verification Code"}
              </Label>
              <Input
                id="mfa-code"
                placeholder={useBackupCode ? "a1b2c3d4" : "000000"}
                maxLength={useBackupCode ? 8 : 6}
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
          </CardContent>

          <CardFooter className="flex flex-col gap-3">
            <Button
              type="submit"
              className="w-full"
              disabled={verifyMutation.isPending}
            >
              {verifyMutation.isPending ? "Verifying..." : "Verify"}
            </Button>
            <div className="flex w-full justify-between">
              <Button
                type="button"
                variant="link"
                size="sm"
                className="text-xs"
                onClick={() => {
                  setUseBackupCode(!useBackupCode);
                  form.reset();
                }}
              >
                {useBackupCode
                  ? "Use authenticator app"
                  : "Use a backup code"}
              </Button>
              {onCancel && (
                <Button
                  type="button"
                  variant="link"
                  size="sm"
                  className="text-xs"
                  onClick={onCancel}
                >
                  Cancel
                </Button>
              )}
            </div>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
