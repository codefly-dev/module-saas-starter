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
} from "@/shared/ui";
import { editUserSchema, type EditUserValues } from "../model/schemas";
import type { User } from "../model/types";
import type { UserEdit } from "../service/mutations";

interface EditUserFormProps {
  open: boolean;
  user: User;
  onSubmit: (edit: UserEdit) => void;
  onCancel: () => void;
  isPending: boolean;
}

/** Edit a user's profile (name) and primary email. The name pair lives in the
 * `profile` map; we merge onto the existing profile so unrelated keys survive. */
export function EditUserForm({ open, user, onSubmit, onCancel, isPending }: EditUserFormProps) {
  const form = useForm<EditUserValues>({
    resolver: zodResolver(editUserSchema),
    defaultValues: {
      firstName: user.profile["first_name"] ?? "",
      lastName: user.profile["last_name"] ?? "",
      primaryEmail: user.primaryEmail,
    },
  });

  const submit = (values: EditUserValues) => {
    const profile = {
      ...user.profile,
      first_name: values.firstName ?? "",
      last_name: values.lastName ?? "",
    };
    // Always send a valid email (the User proto validates it); fall back to the
    // current address when the field was left unchanged/blank.
    onSubmit({ uuid: user.uuid, profile, primaryEmail: values.primaryEmail?.trim() || user.primaryEmail });
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="sm:max-w-[425px]">
        <DialogHeader>
          <DialogTitle>Edit user</DialogTitle>
          <DialogDescription>
            Update the profile for <span className="font-medium">{user.primaryEmail}</span>.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={form.handleSubmit(submit)} className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label htmlFor="firstName">First name</Label>
              <Input id="firstName" {...form.register("firstName")} />
            </div>
            <div className="space-y-2">
              <Label htmlFor="lastName">Last name</Label>
              <Input id="lastName" {...form.register("lastName")} />
            </div>
          </div>
          <div className="space-y-2">
            <Label htmlFor="primaryEmail">Primary email</Label>
            <Input id="primaryEmail" type="email" {...form.register("primaryEmail")} />
            {form.formState.errors.primaryEmail && (
              <p className="text-sm text-destructive">{form.formState.errors.primaryEmail.message}</p>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" disabled={isPending}>
              {isPending ? "Saving..." : "Save changes"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
