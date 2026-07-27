-- Merge a typed ProtoJSON patch without deleting fields introduced by a newer
-- protobuf schema. Objects merge recursively; scalar, enum, list, and map
-- values replace the previous value. Typed application patches never emit
-- JSON null; clear_mask paths use the deletion functions below.
CREATE FUNCTION public.settings_jsonb_deep_merge(stored JSONB, patch JSONB)
RETURNS JSONB
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path = pg_catalog, public
AS $function$
    SELECT CASE
        WHEN jsonb_typeof(stored) = 'object' AND jsonb_typeof(patch) = 'object'
        THEN COALESCE((
            SELECT jsonb_object_agg(
                keys.key,
                CASE
                    WHEN stored ? keys.key AND patch ? keys.key
                    THEN public.settings_jsonb_deep_merge(
                        stored -> keys.key,
                        patch -> keys.key
                    )
                    WHEN patch ? keys.key
                    THEN patch -> keys.key
                    ELSE stored -> keys.key
                END
            )
            FROM (
                SELECT jsonb_object_keys(stored) AS key
                UNION
                SELECT jsonb_object_keys(patch) AS key
            ) AS keys
        ), '{}'::jsonb)
        ELSE patch
    END
$function$;

-- Delete one explicitly configured path and recursively prune parent objects
-- that become empty. This keeps sparse storage aligned with protobuf presence.
CREATE FUNCTION public.settings_jsonb_delete_path(stored JSONB, path TEXT[])
RETURNS JSONB
LANGUAGE plpgsql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path = pg_catalog, public
AS $function$
DECLARE
    child JSONB;
    key TEXT;
BEGIN
    IF COALESCE(array_length(path, 1), 0) = 0
       OR jsonb_typeof(stored) <> 'object' THEN
        RETURN stored;
    END IF;
    key := path[1];
    IF NOT stored ? key THEN
        RETURN stored;
    END IF;
    IF array_length(path, 1) = 1 THEN
        RETURN stored - key;
    END IF;
    child := public.settings_jsonb_delete_path(
        stored -> key,
        path[2:array_length(path, 1)]
    );
    IF child = '{}'::jsonb THEN
        RETURN stored - key;
    END IF;
    RETURN jsonb_set(stored, ARRAY[key], child);
END
$function$;

-- Reset explicitly configured paths back to their protobuf catalog defaults.
-- Paths are validated against the typed field catalog before reaching SQL.
CREATE FUNCTION public.settings_jsonb_delete_paths(stored JSONB, paths TEXT[])
RETURNS JSONB
LANGUAGE plpgsql
IMMUTABLE
STRICT
PARALLEL SAFE
SET search_path = pg_catalog, public
AS $function$
DECLARE
    result JSONB := stored;
    setting_path TEXT;
BEGIN
    FOREACH setting_path IN ARRAY paths LOOP
        result := public.settings_jsonb_delete_path(
            result,
            string_to_array(setting_path, '.')
        );
    END LOOP;
    RETURN result;
END
$function$;

-- Upgrade the original flat common settings shape to the sectioned contract.
-- Existing nested values win so this migration is safe after partial rollout.
UPDATE public.users
SET settings = public.settings_jsonb_deep_merge(
    settings - 'theme',
    jsonb_build_object(
        'appearance',
        jsonb_build_object(
            'theme',
            CASE settings->>'theme'
                WHEN 'system' THEN '"THEME_PREFERENCE_SYSTEM"'::jsonb
                WHEN 'light' THEN '"THEME_PREFERENCE_LIGHT"'::jsonb
                WHEN 'dark' THEN '"THEME_PREFERENCE_DARK"'::jsonb
                ELSE settings->'theme'
            END
        )
    )
)
WHERE settings ? 'theme'
  AND NOT (COALESCE(settings->'appearance', '{}'::jsonb) ? 'theme');

UPDATE public.users SET settings = settings - 'theme' WHERE settings ? 'theme';

UPDATE public.users
SET settings = public.settings_jsonb_deep_merge(
    settings - 'locale',
    jsonb_build_object('regional', jsonb_build_object('locale', settings->'locale'))
)
WHERE settings ? 'locale'
  AND NOT (COALESCE(settings->'regional', '{}'::jsonb) ? 'locale');

UPDATE public.users
SET settings = public.settings_jsonb_deep_merge(
    settings - 'timezone',
    jsonb_build_object('regional', jsonb_build_object('timezone', settings->'timezone'))
)
WHERE settings ? 'timezone'
  AND NOT (COALESCE(settings->'regional', '{}'::jsonb) ? 'timezone');

UPDATE public.users
SET settings = public.settings_jsonb_deep_merge(
    settings - 'date_format',
    jsonb_build_object('regional', jsonb_build_object('date_format', settings->'date_format'))
)
WHERE settings ? 'date_format'
  AND NOT (COALESCE(settings->'regional', '{}'::jsonb) ? 'date_format');

UPDATE public.users
SET settings = public.settings_jsonb_deep_merge(
    settings - 'time_format',
    jsonb_build_object('regional', jsonb_build_object('time_format', settings->'time_format'))
)
WHERE settings ? 'time_format'
  AND NOT (COALESCE(settings->'regional', '{}'::jsonb) ? 'time_format');

UPDATE public.users
SET settings = settings - 'locale' - 'timezone' - 'date_format' - 'time_format'
WHERE settings ?| ARRAY['locale', 'timezone', 'date_format', 'time_format'];

ALTER TABLE public.users
    ADD CONSTRAINT users_settings_typed_json_object
    CHECK (
        jsonb_typeof(settings) = 'object'
        AND octet_length(settings::text) <= 131072
    );
