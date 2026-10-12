DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.executable_artifact_approvals) THEN
  RAISE EXCEPTION 'cannot remove retained executable artifact approvals';
 END IF;
END $$;
DROP TABLE public.executable_artifact_approvals;
DROP FUNCTION public.executable_artifact_no_revival();
