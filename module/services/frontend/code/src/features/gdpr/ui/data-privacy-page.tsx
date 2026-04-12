"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, Trash2, FileArchive, Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Skeleton,
} from "@/shared/ui";
import { formatDate } from "@/shared/lib/utils";
import { gdprQueries } from "../service/queries";
import { gdprMutations } from "../service/mutations";
import type { GDPRRequest } from "../model/types";
import { formatGDPRStatus, formatRequestType } from "../model/transforms";

export function DataPrivacyPage() {
  const queryClient = useQueryClient();
  const { data, isLoading } = useQuery(gdprQueries.requests());
  const requests: GDPRRequest[] =
    (data as { requests?: GDPRRequest[] } | undefined)?.requests ?? [];

  const exportMutation = useMutation({
    mutationFn: () => gdprMutations.requestExport(),
    onSuccess: () => {
      toast.success("Data export requested. You will be notified when ready.");
      queryClient.invalidateQueries({ queryKey: ["gdpr"] });
    },
    onError: () => toast.error("Failed to request data export"),
  });

  const deletionMutation = useMutation({
    mutationFn: () => gdprMutations.requestDeletion(),
    onSuccess: () => {
      toast.success("Account deletion requested.");
      queryClient.invalidateQueries({ queryKey: ["gdpr"] });
    },
    onError: () => toast.error("Failed to request account deletion"),
  });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">Data Privacy</h2>
        <p className="text-muted-foreground">
          Manage your personal data in accordance with GDPR regulations.
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        {/* Export My Data */}
        <Card>
          <CardHeader>
            <div className="flex items-center gap-3">
              <FileArchive className="h-5 w-5 text-primary" />
              <div>
                <CardTitle className="text-lg">Export My Data</CardTitle>
                <CardDescription>
                  Download a copy of all your personal data.
                </CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              We will prepare a ZIP archive containing all your account data,
              activity history, and any content you have created. This may take a
              few minutes.
            </p>
          </CardContent>
          <CardFooter>
            <Button
              onClick={() => exportMutation.mutate()}
              disabled={exportMutation.isPending}
            >
              {exportMutation.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Requesting...
                </>
              ) : (
                <>
                  <Download className="mr-2 h-4 w-4" />
                  Request Export
                </>
              )}
            </Button>
          </CardFooter>
        </Card>

        {/* Delete My Account */}
        <Card className="border-destructive/30">
          <CardHeader>
            <div className="flex items-center gap-3">
              <Trash2 className="h-5 w-5 text-destructive" />
              <div>
                <CardTitle className="text-lg">Delete My Account</CardTitle>
                <CardDescription>
                  Permanently delete your account and all associated data.
                </CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              This action is <strong>irreversible</strong>. All your data,
              including organizations, teams, API keys, and activity history will
              be permanently deleted.
            </p>
          </CardContent>
          <CardFooter>
            <AlertDialog>
              <AlertDialogTrigger
                render={
                  <Button variant="destructive">
                    <Trash2 className="mr-2 h-4 w-4" />
                    Delete Account
                  </Button>
                }
              />
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    Are you absolutely sure?
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    This action cannot be undone. This will permanently delete
                    your account and remove all your data from our servers.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={() => deletionMutation.mutate()}
                    className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                  >
                    {deletionMutation.isPending
                      ? "Requesting..."
                      : "Yes, delete my account"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </CardFooter>
        </Card>
      </div>

      {/* Request History */}
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Request History</CardTitle>
          <CardDescription>
            Past data privacy requests and their status.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : requests.length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">
              No requests yet.
            </p>
          ) : (
            <div className="space-y-2">
              {requests.map((request) => {
                const { label, variant } = formatGDPRStatus(request.status);
                return (
                  <div
                    key={request.id}
                    className="flex items-center justify-between rounded-md border p-3 text-sm"
                  >
                    <div className="flex items-center gap-3">
                      <Badge variant={variant}>{label}</Badge>
                      <span>{formatRequestType(request.type)}</span>
                    </div>
                    <div className="flex items-center gap-4">
                      <span className="text-xs text-muted-foreground">
                        {formatDate(request.createdAt)}
                      </span>
                      {request.downloadUrl && (
                        <Button
                          variant="outline"
                          size="sm"
                          render={
                            <a
                              href={request.downloadUrl}
                              download
                              rel="noopener"
                            />
                          }
                        >
                          <Download className="mr-1 h-3 w-3" />
                          Download
                        </Button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
