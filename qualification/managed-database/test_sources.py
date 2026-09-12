"""Fail closed on source drift or accidental profile/identity substitution."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from stage import BASELINE, MIGRATIONS, stage


class SourcesTest(unittest.TestCase):
    def test_historical_sources_and_baseline_match_provenance(self):
        provenance = json.loads((BASELINE / 'provenance.json').read_text())
        root = Path(__file__).resolve().parents[2]
        self.assertEqual(len(provenance['migration_sha256']), 130)
        for path, digest in provenance['migration_sha256'].items():
            self.assertEqual(hashlib.sha256((root / path).read_bytes()).hexdigest(), digest, path)
        self.assertEqual(hashlib.sha256((BASELINE / '135_managed_baseline.up.sql').read_bytes()).hexdigest(), provenance['baseline_sha256'])

    def test_explicit_profiles_preserve_binding_and_hash_every_byte(self):
        template = {'plan': {'contract-version': 'codefly.dev/postgres-schema-plan/v1', 'database': 'example',
                            'digest': 'old', 'access': {'read-only-role': 'example_ro'},
                            'lineages': [{'label': 'store', 'ledger': 'schema_migrations', 'stage': 'store', 'files': [], 'digest': 'old'}]},
                    'binding': {'owner-role': 'example_migrator', 'read-write-principals': ['example_writer'],
                                'plan-sha256': 'old', 'plan-digest': 'old'}}
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            spec = directory / 'template.json'
            spec.write_text(json.dumps(template))
            for profile in ['fresh', 'upgrade']:
                output = directory / profile
                stage(profile, spec, output)
                actual = json.loads((output / 'spec.json').read_text())
                self.assertEqual(actual['binding']['owner-role'], 'example_migrator')
                self.assertEqual(actual['binding']['read-write-principals'], ['example_writer'])
                self.assertEqual(actual['plan']['database'], 'example')
                self.assertEqual(actual['plan']['access'], template['plan']['access'])
                self.assertEqual(actual['binding']['plan-sha256'], '')
                files = actual['plan']['lineages'][0]['files']
                expected = list(MIGRATIONS.glob('*.sql')) if profile == 'upgrade' else list(BASELINE.glob('*.sql')) + [p for p in MIGRATIONS.glob('*.sql') if int(p.name.split('_')[0]) > 135]
                self.assertEqual({f['name'] for f in files}, {p.name for p in expected})
                for file in files:
                    self.assertEqual(file['digest'], 'sha256:' + hashlib.sha256((output / 'sql/store' / file['name']).read_bytes()).hexdigest())
                with self.assertRaises(ValueError):
                    stage(profile, spec, output)
            with self.assertRaises(ValueError):
                stage('automatic', spec, directory / 'invalid')


if __name__ == '__main__':
    unittest.main()
