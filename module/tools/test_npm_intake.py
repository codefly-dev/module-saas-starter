"""CLI and integrity tests for the distributed npm input preparation tool."""

import base64
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from copy import deepcopy
from pathlib import Path

import npm_intake as intake

DATA = b"reviewed archive bytes"
SRI = "sha512-" + base64.b64encode(hashlib.sha512(DATA).digest()).decode()
TOOL = Path(intake.__file__)


class IntakeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.profile = {"registry": "https://registry.example.com/npm/", "scopes": {"@example": "https://packages.example.com/npm/"}}
        self.manifest = {"name": "example", "dependencies": {"react": "19.2.4"}}
        self.lock = {"lockfileVersion": 3, "packages": {
            "": deepcopy(self.manifest),
            "node_modules/react": {"version": "19.2.4", "resolved": "https://registry.npmjs.org/react/-/react-19.2.4.tgz", "integrity": SRI},
        }}

    def inputs(self):
        for name, value in [("package.json", self.manifest), ("package-lock.json", self.lock), ("profile.json", self.profile)]:
            (self.root / name).write_bytes(intake.encode(value))

    def cli(self, output="prepared", extra=()):
        return subprocess.run([sys.executable, str(TOOL), "--lock", str(self.root / "package-lock.json"),
            "--profile", str(self.root / "profile.json"), "--output", str(self.root / output),
            "--lock-sha256", intake.sha256((self.root / "package-lock.json").read_bytes()),
            "--manifest-sha256", intake.sha256((self.root / "package.json").read_bytes()), *extra],
            capture_output=True, timeout=20)

    def test_cli_two_profiles_retains_graph_and_source(self):
        for index, registry in enumerate(["https://registry.example.com/npm/", "https://mirror.example.net/approved/"]):
            self.profile["registry"] = registry
            self.inputs()
            original = (self.root / "package-lock.json").read_bytes()
            result = self.cli(f"prepared-{index}")
            self.assertEqual(result.returncode, 0, result.stderr)
            output = self.root / f"prepared-{index}"
            lock = json.loads((output / "package-lock.json").read_bytes())
            self.assertEqual(lock["packages"]["node_modules/react"]["resolved"], registry + "react/-/react-19.2.4.tgz")
            lock["packages"]["node_modules/react"]["resolved"] = self.lock["packages"]["node_modules/react"]["resolved"]
            self.assertEqual(lock, self.lock)
            self.assertEqual((self.root / "package-lock.json").read_bytes(), original)
            provenance = json.loads((output / "intake-provenance.json").read_bytes())
            for name, digest in provenance["output_files"].items():
                self.assertEqual(intake.sha256((output / name).read_bytes()), digest)
            self.assertNotEqual(self.cli(f"prepared-{index}").returncode, 0)

    def test_cli_pairs_root_and_workspace_archives(self):
        url = "https://archives.example.com/sdk.tgz"
        self.manifest["dependencies"]["react"] = url
        self.lock["packages"][""] = deepcopy(self.manifest)
        self.lock["packages"]["node_modules/react"]["resolved"] = url
        self.lock["packages"]["packages/widget"] = deepcopy(self.manifest)
        self.lock["packages"]["node_modules/widget"] = {"resolved": "packages/widget", "link": True}
        workspace = self.root / "packages/widget"
        workspace.mkdir(parents=True)
        (workspace / "package.json").write_bytes(intake.encode(self.manifest))
        (self.root / "archive.tgz").write_bytes(DATA)
        entry = {"destination": self.profile["registry"] + "sdk.tgz", "artifact": str(self.root / "archive.tgz"),
                 "sha256": intake.sha256(DATA), "integrity": SRI}
        (self.root / "intakes.json").write_bytes(intake.encode({url: entry}))
        self.inputs()
        result = self.cli(extra=("--intakes", str(self.root / "intakes.json")))
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in ["package.json", "packages/widget/package.json"]:
            self.assertEqual(json.loads((self.root / "prepared" / name).read_bytes())["dependencies"]["react"], entry["destination"])
        (self.root / "archive.tgz").write_bytes(b"different")
        self.assertNotEqual(self.cli("changed", ("--intakes", str(self.root / "intakes.json"))).returncode, 0)
        self.assertFalse((self.root / "changed").exists())

    def test_reject_bad_routes_integrity_and_links(self):
        for url in ["http://registry.npmjs.org/a", "https://secret@registry.npmjs.org/a", "https://registry.npmjs.org/%2e/a", "https://registry.npmjs.org/../a", "https://registry.npmjs.org/a?token=value"]:
            self.lock["packages"]["node_modules/react"]["resolved"] = url
            with self.subTest(url=url), self.assertRaises(ValueError):
                intake.adapt(self.lock, self.profile)
        self.lock["packages"]["node_modules/react"]["resolved"] = "https://registry.npmjs.org/a"
        for integrity in ["", " ", "sha512-bad", "md5-YQ=="]:
            self.lock["packages"]["node_modules/react"]["integrity"] = integrity
            with self.subTest(integrity=integrity), self.assertRaises(ValueError):
                intake.adapt(self.lock, self.profile)
        with self.assertRaises(ValueError):
            intake.routing({"registry": "https://a.example/npm/", "scopes": {"@x": "https://a.example/npm/nested/"}})

    def test_refusal_is_before_output_and_redacts_source(self):
        self.lock["packages"]["node_modules/react"]["resolved"] = "https://private-secret@example.com/a"
        self.inputs()
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(b"private-secret", result.stderr)
        self.assertFalse((self.root / "prepared").exists())

    def test_symlinks_and_hash_drift_are_refused(self):
        self.inputs()
        (self.root / "prepared").symlink_to(self.root / "missing")
        self.assertNotEqual(self.cli().returncode, 0)
        self.assertNotEqual(self.cli("drift", ("--lock-sha256", "0" * 64)).returncode, 0)
        self.assertFalse((self.root / "drift").exists())

    def test_manifest_disagreement_and_unused_mappings(self):
        self.manifest["dependencies"]["react"] = "^20"
        self.inputs()
        self.assertNotEqual(self.cli().returncode, 0)
        with self.assertRaises(ValueError):
            intake.adapt(self.lock, self.profile, {"https://unused.example/a": {}})

    def test_missing_remote_identity_is_refused_before_output(self):
        original = deepcopy(self.lock)
        for index, fields in enumerate([("resolved",), ("integrity",), ("resolved", "integrity")]):
            self.lock = deepcopy(original)
            for field in fields:
                del self.lock["packages"]["node_modules/react"][field]
            self.inputs()
            with self.subTest(fields=fields):
                self.assertNotEqual(self.cli(f"missing-{index}").returncode, 0)
                self.assertFalse((self.root / f"missing-{index}").exists())

    def test_bundle_exception_requires_exact_integrity_pinned_parent(self):
        parent = self.lock["packages"]["node_modules/react"]
        parent["bundleDependencies"] = ["nested"]
        parent["dependencies"] = {"nested": "1.0.0"}
        self.lock["packages"]["node_modules/react/node_modules/nested"] = {"version": "1.0.0", "inBundle": True}
        self.inputs()
        self.assertEqual(self.cli("valid-bundle").returncode, 0)
        parent["bundleDependencies"] = []
        self.inputs()
        self.assertNotEqual(self.cli("undeclared-bundle").returncode, 0)
        self.assertFalse((self.root / "undeclared-bundle").exists())
        parent["bundleDependencies"] = ["nested"]
        del parent["integrity"]
        self.inputs()
        self.assertNotEqual(self.cli("unpinned-bundle").returncode, 0)
        self.assertFalse((self.root / "unpinned-bundle").exists())

    def test_archive_overrides_follow_dependencies_and_preserve_references(self):
        url = "https://archives.example.com/sdk.tgz"
        destination = self.profile["registry"] + "sdk.tgz"
        self.manifest["dependencies"]["react"] = url
        self.manifest["overrides"] = {"react": url, "parent": {"react": url}, "reference": "$react", "other": "^1.0.0"}
        self.lock["packages"][""] = deepcopy(self.manifest)
        self.lock["packages"]["node_modules/react"]["resolved"] = url
        (self.root / "archive.tgz").write_bytes(DATA)
        (self.root / "intakes.json").write_bytes(intake.encode({url: {
            "destination": destination, "artifact": str(self.root / "archive.tgz"),
            "sha256": intake.sha256(DATA), "integrity": SRI,
        }}))
        self.inputs()
        result = self.cli(extra=("--intakes", str(self.root / "intakes.json")))
        self.assertEqual(result.returncode, 0, result.stderr)
        prepared = json.loads((self.root / "prepared/package.json").read_bytes())
        self.assertEqual(prepared["overrides"], {"react": destination, "parent": {"react": destination}, "reference": "$react", "other": "^1.0.0"})
        # Offline npm validates direct override coherence; an unavailable archive
        # may still cause ENOTCACHED, which is not an installation proof.
        result = subprocess.run(["npm", "install", "--package-lock-only", "--ignore-scripts", "--offline",
            "--cache", str(self.root / "cache"), "--userconfig", str(self.root / "unused-user-config"),
            "--globalconfig", str(self.root / "unused-global-config")], cwd=self.root / "prepared",
            capture_output=True, timeout=20)
        self.assertNotIn(b"EOVERRIDE", result.stderr)

    def test_unmapped_or_archive_qualified_overrides_refuse_before_output(self):
        for index, overrides in enumerate([
            {"react": "https://unmapped.example.com/react.tgz"},
            {"parent": {"react": "https://unmapped.example.com/react.tgz"}},
            {"react@https://archives.example.com/react.tgz": "19.2.4"},
        ]):
            self.manifest["overrides"] = overrides
            self.lock["packages"][""] = deepcopy(self.manifest)
            self.inputs()
            with self.subTest(overrides=overrides):
                self.assertNotEqual(self.cli(f"override-{index}").returncode, 0)
                self.assertFalse((self.root / f"override-{index}").exists())

    def test_version_overrides_and_dependency_references_are_unchanged(self):
        self.manifest["overrides"] = {"react": "$react", "@types/react": "19.2.7", "parent": {"other": "^1.0.0"}}
        self.lock["packages"][""] = deepcopy(self.manifest)
        self.inputs()
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        prepared = json.loads((self.root / "prepared/package.json").read_bytes())
        self.assertEqual(prepared["overrides"], self.manifest["overrides"])

    def test_selected_real_inputs_refuse_unmapped_archives(self):
        inputs = os.environ.get("CODEFLY_NPM_INPUT_LOCKS")
        if not inputs:
            self.skipTest("set CODEFLY_NPM_INPUT_LOCKS to a JSON list of real lock paths")
        for value in json.loads(inputs):
            source = Path(value).resolve()
            lock = json.loads(intake.regular(source))
            # Inspect every remote entry, not merely the first exceptional URL.
            for record in lock["packages"].values():
                if record.get("resolved") and not record.get("link"):
                    intake.safe_url(record["resolved"])
                    intake.sri(record.get("integrity"))
            for index, registry in enumerate(["https://registry.example.com/npm/", "https://mirror.example.net/approved/"]):
                profile = self.root / "real-profile.json"
                profile.write_bytes(intake.encode({"registry": registry}))
                output = self.root / f"real-{index}"
                result = subprocess.run([sys.executable, str(TOOL), "--lock", str(source),
                    "--profile", str(profile), "--output", str(output),
                    "--lock-sha256", intake.sha256(intake.regular(source)),
                    "--manifest-sha256", intake.sha256(intake.regular(source.with_name("package.json")))],
                    capture_output=True, timeout=20)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())
            print(f"checked {len(lock['packages'])} real lock entries; unmapped archive refused for two profiles")


if __name__ == "__main__":
    unittest.main()
