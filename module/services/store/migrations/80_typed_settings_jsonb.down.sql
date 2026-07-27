ALTER TABLE public.users
    DROP CONSTRAINT users_settings_typed_json_object;

-- Keep sectioned values for forward compatibility while restoring the flat
-- keys expected by a downgraded application.
UPDATE public.users
SET settings = settings || jsonb_build_object('theme', settings#>'{appearance,theme}')
WHERE settings#>'{appearance,theme}' IS NOT NULL;

UPDATE public.users
SET settings = settings
    || jsonb_strip_nulls(jsonb_build_object(
        'locale', settings#>'{regional,locale}',
        'timezone', settings#>'{regional,timezone}',
        'date_format', settings#>'{regional,date_format}',
        'time_format', settings#>'{regional,time_format}'
    ))
WHERE settings ? 'regional';

DROP FUNCTION public.settings_jsonb_delete_paths(JSONB, TEXT[]);
DROP FUNCTION public.settings_jsonb_delete_path(JSONB, TEXT[]);
DROP FUNCTION public.settings_jsonb_deep_merge(JSONB, JSONB);
