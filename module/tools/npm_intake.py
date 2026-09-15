"""Prepare paired npm inputs for a selected HTTPS registry, without acquisition.

The caller supplies reviewed source hashes and routing. This command neither
downloads packages nor asserts feed approval; npm ci must verify the original
integrities when the prepared inputs reach an authenticated build lane.
"""

import argparse
import base64
import hashlib
import json
import re
from copy import deepcopy
from pathlib import Path
from urllib.parse import urlsplit

LENGTHS = {"sha512": 64, "sha384": 48, "sha256": 32, "sha1": 20}
GROUPS = ("dependencies", "devDependencies", "optionalDependencies", "peerDependencies")


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def regular(path):
    path = Path(path).absolute()
    if any(p.is_symlink() for p in (path, *path.parents)) or not path.is_file():
        raise ValueError("Require a regular file without symlink ancestors")
    return path.read_bytes()


def sri(value, artifact=None):
    if not isinstance(value, str) or not value.strip():
        raise ValueError("Every remote package requires original lock integrity")
    parsed = []
    for token in value.split():
        try:
            algorithm, encoded = token.split("-", 1)
            expected = base64.b64decode(encoded, validate=True)
            if len(expected) != LENGTHS[algorithm]:
                raise ValueError()
        except (ValueError, KeyError):
            raise ValueError("Unsupported or malformed original integrity") from None
        parsed.append((algorithm, expected))
    if artifact is not None and any(hashlib.new(a, artifact).digest() != h for a, h in parsed):
        raise ValueError("Intake bytes do not match original lock integrity")


def safe_url(value):
    if not isinstance(value, str) or re.search(r"[\s\\]", value):
        raise ValueError("Malformed package source URL")
    parts = urlsplit(value)
    if (parts.scheme != "https" or not parts.hostname or parts.username is not None
            or parts.password is not None or parts.port or parts.query or parts.fragment):
        raise ValueError("Require credential-free HTTPS package source")
    if "%" in parts.path or any(x in {".", ".."} for x in parts.path.split("/")) or "//" in parts.path:
        raise ValueError("Ambiguous package URL path")
    return parts


def routing(profile):
    if not isinstance(profile, dict) or set(profile) - {"registry", "scopes", "install_options"}:
        raise ValueError("Unsupported routing fields")
    registry = profile["registry"]
    scopes = profile.get("scopes", {})
    if not isinstance(scopes, dict):
        raise ValueError("Scopes must be explicit routes")
    for scope in scopes:
        if not re.fullmatch(r"@[a-z0-9][a-z0-9._-]*", scope):
            raise ValueError("Invalid package scope")
    routes = {registry, *scopes.values()}
    for route in routes:
        safe_url(route)
        if not route.endswith("/"):
            raise ValueError("Registry route requires a trailing slash")
    for a in routes:
        if any(a != b and a.startswith(b) for b in routes):
            raise ValueError("Overlapping registry routes are ambiguous")
    options = profile.get("install_options", {})
    if not isinstance(options, dict) or any(
        k not in {"legacy-peer-deps", "strict-peer-deps"} or type(v) is not bool
        for k, v in options.items()
    ):
        raise ValueError("Unsupported install option; credentials belong outside the inputs")
    return registry, scopes, routes, options


def safe_relative(value):
    if (not isinstance(value, str) or not re.fullmatch(r"[a-zA-Z0-9_./@-]+", value)
            or value.startswith("/") or any(x in {"", ".", ".."} for x in value.split("/"))):
        raise ValueError("Unsafe workspace path")
    return value


def adapt(lock, profile, intakes=None):
    registry, scopes, routes, _ = routing(profile)
    if lock.get("lockfileVersion") != 3 or not isinstance(lock.get("packages"), dict):
        raise ValueError("Require npm lockfile v3 packages")
    result, changes, used = deepcopy(lock), [], set()
    intakes = intakes or {}
    packages = result["packages"]
    if any(not isinstance(package, dict) for package in packages.values()):
        raise ValueError("Every locked package must be a record")
    workspaces = {package.get("resolved") for package in packages.values() if package.get("link") is True}
    for name, package in result["packages"].items():
        if name:
            safe_relative(name)
        resolved = package.get("resolved")
        if package.get("link") is True:
            safe_relative(resolved)
            if (resolved not in packages or "node_modules" in resolved.split("/")
                    or packages[resolved].get("resolved") is not None or packages[resolved].get("link")):
                raise ValueError("Workspace link lacks a locked workspace")
            continue
        if resolved is None:
            if not name or (name in workspaces and "node_modules" not in name.split("/")):
                continue
            # Bundled bytes are identified by their enclosing archive, not an
            # independent URL. Require the exact locked parent's declaration;
            # an arbitrary inBundle flag cannot waive artifact identity.
            parent, separator, child = name.rpartition("/node_modules/")
            owner = packages.get(parent, {})
            if (package.get("inBundle") is True and separator
                    and owner.get("resolved") and not owner.get("link")
                    and child in owner.get("bundleDependencies", [])
                    and isinstance(owner.get("bundleDependencies"), list)
                    and child in owner.get("dependencies", {})):
                safe_url(owner["resolved"])
                sri(owner.get("integrity"))
                continue
            raise ValueError("Installed package lacks a reviewed source and original integrity")
        parts = safe_url(resolved)
        sri(package.get("integrity"))
        scope = parts.path.lstrip("/").split("/", 1)[0]
        if parts.hostname == "registry.npmjs.org":
            destination = scopes.get(scope, registry) + parts.path.lstrip("/")
        elif any(resolved.startswith(r) and resolved != r for r in routes):
            destination = resolved
        else:
            entry = intakes.get(resolved)
            if not isinstance(entry, dict) or set(entry) != {"destination", "artifact", "sha256", "integrity"}:
                raise ValueError("Non-npmjs source requires reviewed byte-verified intake mapping")
            destination = entry["destination"]
            safe_url(destination)
            if not any(destination.startswith(r) and destination != r for r in routes):
                raise ValueError("Intake destination is outside selected registry routes")
            if entry["integrity"] != package["integrity"]:
                raise ValueError("Intake mapping changes original integrity")
            artifact = regular(entry["artifact"])
            if not isinstance(entry["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"]) or sha256(artifact) != entry["sha256"]:
                raise ValueError("Intake artifact SHA256 mismatch")
            sri(package["integrity"], artifact)
            used.add(resolved)
        package["resolved"] = destination
        changes.append({"package": name, "source": resolved, "destination": destination, "integrity": package["integrity"]})
    if set(intakes) != used:
        raise ValueError("Unused intake mappings are refused")
    return result, changes


def adapt_manifest(manifest, root, transformed_root, changes):
    result = deepcopy(manifest)
    routes = {item["source"]: item["destination"] for item in changes}
    for group in GROUPS:
        if manifest.get(group, {}) != root.get(group, {}):
            raise ValueError("Manifest and lock dependency declarations differ")
        for name, value in manifest.get(group, {}).items():
            if not isinstance(value, str):
                raise ValueError("Dependency declaration must be a string")
            if value.startswith(("https:", "http:", "git:", "github:", "git+")):
                if value not in routes:
                    raise ValueError("Direct source dependency lacks reviewed intake")
                result[group][name] = routes[value]
                transformed_root[group][name] = routes[value]
            elif value.startswith("file:"):
                raise ValueError("Local archive dependencies require a separate reviewed source change")
    if "overrides" in manifest:
        result["overrides"] = adapt_overrides(manifest["overrides"], routes)
        if "overrides" in root:
            if root["overrides"] != manifest["overrides"]:
                raise ValueError("Manifest and lock overrides differ")
            transformed_root["overrides"] = deepcopy(result["overrides"])
    return result


def adapt_overrides(value, routes):
    """Preserve npm's nested and $reference forms; route exact archive values."""
    source_prefixes = ("https:", "http:", "git:", "github:", "git+", "file:")
    if isinstance(value, str):
        if value.startswith(source_prefixes):
            if value not in routes:
                raise ValueError("Archive override lacks reviewed intake")
            return routes[value]
        return value
    if not isinstance(value, dict):
        raise ValueError("Override must be a version/reference or nested mapping")
    result = {}
    for selector, override in value.items():
        if not isinstance(selector, str) or any(prefix in selector for prefix in source_prefixes):
            raise ValueError("Archive-qualified override selectors are unsupported")
        result[selector] = adapt_overrides(override, routes)
    return result


def encode(value):
    return (json.dumps(value, indent=2) + "\n").encode()


def package(source, output, profile, lock_sha256, manifest_sha256, mappings=None):
    source, output = Path(source), Path(output).absolute()
    source_bytes = regular(source)
    manifest_bytes = regular(source.with_name("package.json"))
    if sha256(source_bytes) != lock_sha256 or sha256(manifest_bytes) != manifest_sha256:
        raise ValueError("Reviewed source hash mismatch")
    lock = json.loads(source_bytes)
    transformed, changes = adapt(lock, profile, mappings)
    registry, scopes, _, options = routing(profile)
    files, sources = {}, {"package-lock.json": sha256(source_bytes)}
    # Root and every locked local workspace get paired declarations. Source
    # copies remain caller-owned; these prepared manifests overlay a fresh copy.
    for name, record in lock["packages"].items():
        if name and ("node_modules" in name.split("/") or record.get("resolved") is not None):
            continue
        relative = (name + "/" if name else "") + "package.json"
        original = regular(source.parent / relative)
        sources[relative] = sha256(original)
        files[relative] = encode(adapt_manifest(json.loads(original), record, transformed["packages"][name], changes))
    if "package.json" not in files:
        raise ValueError("Root package entry is required")
    files["package-lock.json"] = encode(transformed)
    files[".npmrc"] = (f"registry={registry}\n" + "".join(f"{s}:registry={r}\n" for s, r in sorted(scopes.items()))
                       + "".join(f"{k}={str(v).lower()}\n" for k, v in sorted(options.items()))).encode()
    provenance = {
        "source_files": sources,
        "output_files": {name: sha256(value) for name, value in files.items()},
        "routing": profile, "changes": changes,
        "intakes": {url: {k: v for k, v in entry.items() if k != "artifact"} for url, entry in (mappings or {}).items()},
        "tool_sha256": sha256(regular(__file__)),
        "status": "prepared only; feed presence, approval, installation and runtime unverified",
    }
    # Validate before creating anything; refuse even a dangling output symlink.
    if output.exists() or any(p.is_symlink() for p in (output, *output.parents)):
        raise ValueError("Output must be a new directory without symlink ancestors")
    output.mkdir(parents=True, exist_ok=False)
    for name, value in files.items():
        target = output / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(value)
    (output / "intake-provenance.json").write_bytes(encode(provenance))
    return provenance


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--profile", required=True, type=Path)
    parser.add_argument("--lock-sha256", required=True)
    parser.add_argument("--manifest-sha256", required=True)
    parser.add_argument("--intakes", type=Path)
    args = parser.parse_args()
    try:
        package(args.lock, args.output, json.loads(regular(args.profile)), args.lock_sha256,
                args.manifest_sha256, json.loads(regular(args.intakes)) if args.intakes else None)
    except (ValueError, OSError, TypeError, KeyError, AttributeError):
        raise SystemExit("npm intake refused; validate source hashes, routing, paired declarations and original integrity") from None
