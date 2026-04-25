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

export function APIKeyForm({ orgId }: { orgId: string }) {
  const [open, setOpen] = useState(false);
  const [keyName, setKeyName] = useState("");
  const [environment, setEnvironment] = useState("1");

  // State for showing the plaintext key after creation
  const [plaintextKey, setPlaintextKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const createKey = useCreateAPIKey();

  function reset() {
    setKeyName("");
    setEnvironment("1");
    setPlaintextKey(null);
    setCopied(false);
  }

  function handleSubmit() {
    if (!keyName.trim() || !orgId) return;
    createKey.mutate(
      {
        organizationId: orgId,
        name: keyName.trim(),
        environment: Number(environment),
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
