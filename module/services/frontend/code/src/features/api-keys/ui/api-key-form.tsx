"use client";

import { useState } from "react";
import { Plus, Copy, Check } from "lucide-react";
import { toast } from "sonner";
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui";
import { useCreateAPIKey } from "../service/mutations";

// SCOPE_PRESETS — the operator-facing label maps to a list of
// `{resource, action}` scope objects the api persists with the key.
// Wildcards (* in either segment) are honoured by requireScope, so
// "Read all" → `*:read` covers any read-tagged handler.
const SCOPE_PRESETS = [
  {
    id: "read_only",
    label: "Read-only access",
    description: "Read users, orgs, audit logs, webhooks, and entitlements. Cannot mutate.",
    scopes: [{ resource: "*", action: "read" }],
  },
  {
    id: "read_write",
    label: "Read & write",
    description: "Full app access. Use for backend integrations that need to mutate data.",
    scopes: [
      { resource: "*", action: "read" },
      { resource: "*", action: "write" },
    ],
  },
  {
    id: "webhooks_only",
    label: "Webhook management",
    description: "Manage outbound webhooks (create / replay / rotate). Can also list audit events.",
    scopes: [
      { resource: "webhooks", action: "read" },
      { resource: "webhooks", action: "write" },
      { resource: "audit", action: "read" },
    ],
  },
  {
    id: "no_scopes",
    label: "No scopes",
    description: "For sandboxing — every scoped handler will reject the key. Useful for testing scope enforcement.",
    scopes: [],
  },
] as const;

type ScopePreset = (typeof SCOPE_PRESETS)[number]["id"];

export function APIKeyForm({ orgId }: { orgId: string }) {
  const [open, setOpen] = useState(false);
  const [keyName, setKeyName] = useState("");
  const [environment, setEnvironment] = useState("1");
  const [scopePreset, setScopePreset] = useState<ScopePreset>("read_only");

  // State for showing the plaintext key after creation
  const [plaintextKey, setPlaintextKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const createKey = useCreateAPIKey();

  function reset() {
    setKeyName("");
    setEnvironment("1");
    setScopePreset("read_only");
    setPlaintextKey(null);
    setCopied(false);
  }

  function handleSubmit() {
    if (!keyName.trim() || !orgId) return;
    const preset = SCOPE_PRESETS.find((p) => p.id === scopePreset);
    createKey.mutate(
      {
        organizationId: orgId,
        name: keyName.trim(),
        environment: Number(environment),
        scopes: preset ? [...preset.scopes] : [],
      },
      {
        onSuccess: (data) => {
          toast.success(`Key "${keyName.trim()}" created`);
          setPlaintextKey(data.plaintextKey);
        },
        onError: () => toast.error("Failed to create API key"),
      },
    );
  }

  function handleCopy() {
    if (!plaintextKey) return;
    navigator.clipboard
      .writeText(plaintextKey)
      .then(() => {
        // Stays true once set — gates the Done button below. Resetting
        // it on a timeout (the old behaviour) would re-disable Done a
        // few seconds later, which is the wrong UX: once the operator
        // has the secret, that's done forever.
        setCopied(true);
        toast.success("Key copied to clipboard");
      })
      .catch(() => toast.error("Copy failed — copy it manually"));
  }

  function handleClose() {
    reset();
    setOpen(false);
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) handleClose(); else setOpen(true); }}>
      <DialogTrigger render={<Button disabled={!orgId} />}>
        <Plus className="mr-2 h-4 w-4" />
        Create Key
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        {plaintextKey ? (
          <>
            <DialogHeader>
              <DialogTitle>API Key Created</DialogTitle>
              <DialogDescription>
                Copy your API key now. You will not be able to see it again.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 py-4">
              <div className="flex items-center gap-2 rounded-md border bg-muted p-3">
                <code className="flex-1 break-all text-sm font-mono">
                  {plaintextKey}
                </code>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={handleCopy}
                >
                  {copied ? (
                    <Check className="h-4 w-4 text-green-500" />
                  ) : (
                    <Copy className="h-4 w-4" />
                  )}
                </Button>
              </div>
              <p className="text-sm text-destructive font-medium">
                This key will not be shown again. Make sure to copy it.
              </p>
            </div>
            <DialogFooter>
              <Button onClick={handleClose} disabled={!copied}>
                {copied ? "Done" : "Copy the key first"}
              </Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create API Key</DialogTitle>
              <DialogDescription>
                Generate a new API key for programmatic access.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 py-4">
              <div className="space-y-2">
                <Label htmlFor="key-name">Name</Label>
                <Input
                  id="key-name"
                  placeholder="e.g. production-backend"
                  value={keyName}
                  onChange={(e) => setKeyName(e.target.value)}
                />
              </div>

              <div className="space-y-2">
                <Label>Environment</Label>
                <Select value={environment} onValueChange={(v) => { if (v) setEnvironment(v); }}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="1">Live</SelectItem>
                    <SelectItem value="2">Test</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label>Scopes</Label>
                <div className="rounded-md border divide-y">
                  {SCOPE_PRESETS.map((preset) => (
                    <label
                      key={preset.id}
                      className="flex items-start gap-3 p-3 cursor-pointer hover:bg-accent/30"
                    >
                      <input
                        type="radio"
                        name="scope-preset"
                        value={preset.id}
                        checked={scopePreset === preset.id}
                        onChange={() => setScopePreset(preset.id)}
                        className="mt-0.5"
                      />
                      <div className="space-y-0.5">
                        <div className="font-medium text-sm">{preset.label}</div>
                        <div className="text-xs text-muted-foreground">
                          {preset.description}
                        </div>
                      </div>
                    </label>
                  ))}
                </div>
                <p className="text-xs text-muted-foreground">
                  Wildcard scopes (<code>*:read</code> / <code>*:*</code>)
                  cover whole categories. Backend handlers gate via
                  <code className="ml-1">requireScope</code>.
                </p>
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={handleClose}>
                Cancel
              </Button>
              <Button
                onClick={handleSubmit}
                disabled={createKey.isPending || !keyName.trim()}
              >
                {createKey.isPending ? "Creating..." : "Create Key"}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
