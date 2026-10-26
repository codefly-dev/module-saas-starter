-- A module installation's scope follows its module's grant (envelope ∩
-- manifest) as it moves, until a person narrows it by writing the axes out.
-- The rows written so far are snapshots of the grant at install time, frozen
-- while every later generation moved, so each one is reset to follow. A
-- narrowing a person wants is written again, axis by axis, through
-- NarrowInstallationScope.
UPDATE public.installations
   SET scope = '{"capabilities": true, "follows": true}'::jsonb
 WHERE kind = 'module'
   AND status = 'active'
   AND coalesce((scope->>'capabilities')::boolean, false);
