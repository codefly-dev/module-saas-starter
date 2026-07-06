"use client";

import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
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
  Textarea,
} from "@/shared/ui";
import { createTeamSchema, type CreateTeamValues } from "../model/schemas";

interface TeamFormProps {
  open: boolean;
  /** "create" (default) or "edit" — an edit form seeds `initial` and relabels. */
  mode?: "create" | "edit";
  initial?: { name: string; description?: string };
  onSubmit: (values: CreateTeamValues) => void;
  onCancel: () => void;
  isPending: boolean;
}

export function TeamForm({ open, mode = "create", initial, onSubmit, onCancel, isPending }: TeamFormProps) {
  const editing = mode === "edit";
  const form = useForm<CreateTeamValues>({
    resolver: zodResolver(createTeamSchema),
    defaultValues: { name: initial?.name ?? "", description: initial?.description ?? "" },
  });

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="sm:max-w-[425px]">
        <DialogHeader>
          <DialogTitle>{editing ? "Rename team" : "Create Team"}</DialogTitle>
          <DialogDescription>
            {editing
              ? "Update this team's name and description."
              : "Add a new team to the selected organization."}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="team-name">Name</Label>
            <Input
              id="team-name"
              placeholder="Engineering"
              {...form.register("name")}
            />
            {form.formState.errors.name && (
              <p className="text-sm text-destructive">
                {form.formState.errors.name.message}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="team-desc">Description</Label>
            <Textarea
              id="team-desc"
              placeholder="Optional description..."
              {...form.register("description")}
            />
            {form.formState.errors.description && (
              <p className="text-sm text-destructive">
                {form.formState.errors.description.message}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" disabled={isPending}>
              {isPending ? (editing ? "Saving..." : "Creating...") : editing ? "Save" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
