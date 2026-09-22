"""Fail closed on baseline drift or an unrecorded ledger."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from stage import MIGRATIONS, PROVENANCE, ledger_files, stage


class SourcesTest(unittest.TestCase):
    def test_baseline_matches_its_provenance_and_names_what_it_folded(self):
        provenance = json.loads(PROVENANCE.read_text())
        root = Path(__file__).resolve().parents[2]
        baseline = root / provenance['baseline']
        self.assertEqual(hashlib.sha256(baseline.read_bytes()).hexdigest(), provenance['baseline_sha256'])
        self.assertEqual(provenance['replaced_migration_count'] * 2, len(provenance['replaced_sha256']))
        # The folded files are gone; the record of them is the only trace.
        for path in provenance['replaced_sha256']:
            self.assertFalse((root / path).exists(), path)
        self.assertIn(baseline.name, ledger_files())

    def test_an_edited_baseline_is_refused(self):
        provenance = json.loads(PROVENANCE.read_text())
        baseline = Path(__file__).resolve().parents[2] / provenance['baseline']
        original = baseline.read_bytes()
        try:
            baseline.write_bytes(original + b'-- edited\n')
            with self.assertRaises(ValueError):
                ledger_files()
        finally:
            baseline.write_bytes(original)

    def test_staging_preserves_binding_and_hashes_every_byte(self):
        template = {'plan': {'contract-version': 'codefly.dev/postgres-schema-plan/v1', 'database': 'example',
                            'digest': 'old', 'access': {'read-only-role': 'example_ro'},
                            'lineages': [{'label': 'store', 'ledger': 'schema_migrations', 'stage': 'store', 'files': [], 'digest': 'old'}]},
                    'binding': {'owner-role': 'example_migrator', 'read-write-principals': ['example_writer'],
                                'plan-sha256': 'old', 'plan-digest': 'old'}}
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            spec = directory / 'template.json'
            spec.write_text(json.dumps(template))
            output = directory / 'staged'
            stage(spec, output)
            actual = json.loads((output / 'spec.json').read_text())
            self.assertEqual(actual['binding']['owner-role'], 'example_migrator')
            self.assertEqual(actual['binding']['read-write-principals'], ['example_writer'])
            self.assertEqual(actual['plan']['database'], 'example')
            self.assertEqual(actual['plan']['access'], template['plan']['access'])
            self.assertEqual(actual['binding']['plan-sha256'], '')
            files = actual['plan']['lineages'][0]['files']
            self.assertEqual({f['name'] for f in files}, {p.name for p in MIGRATIONS.glob('*.sql')})
            for file in files:
                self.assertEqual(file['digest'], 'sha256:' + hashlib.sha256((output / 'sql/store' / file['name']).read_bytes()).hexdigest())
            with self.assertRaises(ValueError):
                stage(spec, output)


if __name__ == '__main__':
    unittest.main()
