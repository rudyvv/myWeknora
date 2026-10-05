import hashlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
TOOL_PATH = REPO_ROOT / "tools" / "acceptance" / "validate_source_representative_questions.py"
SPEC = importlib.util.spec_from_file_location("source_question_validator", TOOL_PATH)
validator = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(validator)


class SourceRepresentativeQuestionTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.source = self.root / "sample.java"
        self.source.write_text("public void lookupThing() {}\n", encoding="utf-8")
        canonical = self.source.read_bytes().replace(b"\r\n", b"\n").replace(b"\r", b"\n")
        self.digest = hashlib.sha256(canonical).hexdigest()
        self.roots = {"nsb": self.root}

    def tearDown(self):
        self.temp_dir.cleanup()

    def make_dataset(self):
        categories = ["symbol_path"] * 10 + ["business_chain"] * 10 + ["frontend_sql"] * 10
        return {
            "schema_version": 1,
            "status": "draft_for_human_confirmation",
            "hash_mode": "sha256_utf8_lf",
            "repositories": [{"id": "nsb", "kind": "representative_repo", "commit": "a" * 40}],
            "questions": [
                {
                    "id": f"Q-{index + 1:02d}",
                    "category": category,
                    "question": f"How does question {index + 1} work?",
                    "repository": "nsb",
                    "source_kind": "representative_repo",
                    "evidence": [
                        {
                            "path": "sample.java",
                            "start_line": 1,
                            "end_line": 1,
                            "symbol": "lookupThing",
                            "sha256": self.digest,
                            "rationale": "The declaration is directly present in this span.",
                        }
                    ],
                }
                for index, category in enumerate(categories)
            ],
        }

    def errors(self, dataset):
        return validator.validate_dataset(dataset, self.roots, check_revisions=False)

    def test_accepts_exactly_thirty_questions_in_three_balanced_groups(self):
        self.assertEqual([], self.errors(self.make_dataset()))

    def test_hash_is_independent_of_source_line_endings(self):
        dataset = self.make_dataset()
        self.source.write_bytes(b"public void lookupThing() {}\r\n")

        self.assertEqual([], self.errors(dataset))

    def test_requires_explicit_utf8_lf_hash_mode(self):
        missing_mode = self.make_dataset()
        del missing_mode["hash_mode"]
        wrong_mode = self.make_dataset()
        wrong_mode["hash_mode"] = "sha256_raw_bytes"

        for dataset in (missing_mode, wrong_mode):
            with self.subTest(hash_mode=dataset.get("hash_mode")):
                errors = self.errors(dataset)
                self.assertTrue(any("hash_mode" in error for error in errors), errors)

    def test_rejects_unrecognized_fields_at_every_schema_level(self):
        cases = [
            ("dataset", lambda dataset: dataset.update({"future_dataset_field": "value"})),
            (
                "repository",
                lambda dataset: dataset["repositories"][0].update({"future_repository_field": "value"}),
            ),
            (
                "question",
                lambda dataset: dataset["questions"][0].update({"future_question_field": "value"}),
            ),
            (
                "evidence",
                lambda dataset: dataset["questions"][0]["evidence"][0].update(
                    {"future_evidence_field": "value"}
                ),
            ),
        ]

        for level, inject in cases:
            with self.subTest(level=level):
                dataset = self.make_dataset()
                inject(dataset)
                errors = self.errors(dataset)
                self.assertTrue(any("unknown field" in error.lower() for error in errors), errors)

    def test_rejects_source_body_fields_even_when_they_are_not_in_the_legacy_denylist(self):
        dataset = self.make_dataset()
        entire_source = self.source.read_text(encoding="utf-8")
        dataset["questions"][0]["raw_source"] = entire_source
        dataset["questions"][1]["evidence"][0]["full_source"] = entire_source

        errors = self.errors(dataset)

        self.assertTrue(any("raw_source" in error for error in errors), errors)
        self.assertTrue(any("full_source" in error for error in errors), errors)

    def test_rejects_duplicate_ids_and_incorrect_group_counts(self):
        dataset = self.make_dataset()
        dataset["questions"][1]["id"] = dataset["questions"][0]["id"]
        dataset["questions"].pop()

        errors = self.errors(dataset)

        self.assertTrue(any("duplicate" in error.lower() for error in errors), errors)
        self.assertTrue(any("count" in error.lower() for error in errors), errors)

    def test_rejects_paths_outside_root_and_invalid_evidence_locators(self):
        dataset = self.make_dataset()
        dataset["questions"][0]["evidence"][0]["path"] = "../outside.java"
        dataset["questions"][1]["evidence"][0]["start_line"] = 2
        dataset["questions"][2]["evidence"][0]["symbol"] = "missingSymbol"

        errors = self.errors(dataset)

        self.assertTrue(any("path" in error.lower() for error in errors), errors)
        self.assertTrue(any("line" in error.lower() for error in errors), errors)
        self.assertTrue(any("symbol" in error.lower() or "locator" in error.lower() for error in errors), errors)

    def test_rejects_changed_hashes_and_result_or_source_body_fields(self):
        dataset = self.make_dataset()
        dataset["questions"][0]["evidence"][0]["sha256"] = "0" * 64
        dataset["questions"][1]["evidence"][0]["source_text"] = "copied source"
        dataset["questions"][2]["retrieved_matches"] = ["not ground truth"]
        dataset["questions"][3]["expected_answer"] = "not part of the benchmark"

        errors = self.errors(dataset)

        self.assertTrue(any("hash" in error.lower() for error in errors), errors)
        self.assertTrue(any("source_text" in error for error in errors), errors)
        self.assertTrue(any("retrieved_matches" in error for error in errors), errors)
        self.assertTrue(any("expected_answer" in error for error in errors), errors)

    def test_rejects_mismatched_source_kind_and_unpinned_commit(self):
        dataset = self.make_dataset()
        dataset["questions"][0]["source_kind"] = "approved_supplement"
        dataset["repositories"][0]["commit"] = "not-a-commit"

        errors = self.errors(dataset)

        self.assertTrue(any("source_kind" in error for error in errors), errors)
        self.assertTrue(any("commit" in error.lower() for error in errors), errors)

    def test_accepts_cross_repository_evidence_with_explicit_repository_owner(self):
        dataset = self.make_dataset()
        dataset["repositories"].append(
            {"id": "mobile", "kind": "representative_repo", "commit": "b" * 40}
        )
        dataset["questions"][0]["evidence"][0]["repository"] = "mobile"

        errors = validator.validate_dataset(
            dataset,
            {"nsb": self.root, "mobile": self.root},
            check_revisions=False,
        )

        self.assertEqual([], errors)

    def test_cli_reports_malformed_json(self):
        malformed = self.root / "malformed.json"
        malformed.write_text("{not-json", encoding="utf-8")

        result = subprocess.run(
            [sys.executable, str(TOOL_PATH), str(malformed), "--no-revision-check"],
            capture_output=True,
            text=True,
            check=False,
        )

        self.assertEqual(2, result.returncode)
        self.assertIn("cannot read valid JSON", result.stderr)


if __name__ == "__main__":
    unittest.main()
