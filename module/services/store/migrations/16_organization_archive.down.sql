-- Rolling back removes the archive record. An organization archived while this
-- migration was applied keeps no members — archiving removed them — but once the
-- trigger is gone nothing stops a membership being added to it again, and
-- nothing distinguishes it from a live organization any more.

DROP TRIGGER IF EXISTS organization_members_refuse_archived_organization ON public.organization_members;
DROP FUNCTION IF EXISTS public.refuse_archived_organization_membership();

ALTER TABLE public.organizations
    DROP COLUMN IF EXISTS archived_by,
    DROP COLUMN IF EXISTS archived_at;
