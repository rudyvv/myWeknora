#!/usr/bin/env python3
"""Validate pinned source evidence for the representative-question dataset."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path, PurePosixPath, PureWindowsPath
from typing import Any


EXPECTED_COUNTS = {
    "symbol_path": 10,
    "business_chain": 10,
    "frontend_sql": 10,
}
VALID_KINDS = {"representative_repo", "approved_supplement"}
COMMIT_RE = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
HASH_MODE = "sha256_utf8_lf"
ALLOWED_FIELDS = {
    "dataset": frozenset({"schema_version", "status", "description", "hash_mode", "repositories", "questions"}),
    "repository": frozenset({"id", "kind", "commit", "path_prefixes", "verify_current_revision"}),
    "question": frozenset({"id", "category", "question", "repository", "source_kind", "evidence"}),
    "evidence": frozenset({"repository", "path", "start_line", "end_line", "symbol", "sha256", "rationale"}),
}


def _is_mapping(value: Any) -> bool:
    return isinstance(value, dict)


def _safe_relative_path(value: Any) -> bool:
    if not isinstance(value, str) or not value or "\\" in value:
        return False
    posix = PurePosixPath(value)
    windows = PureWindowsPath(value)
    return not posix.is_absolute() and not windows.is_absolute() and not windows.drive and all(
        part not in {"", ".", ".."} for part in value.split("/")
    )


def _within_prefix(path: str, prefixes: Any) -> bool:
    if not prefixes:
        return True
    return isinstance(prefixes, list) and any(
        isinstance(prefix, str)
        and prefix
        and (path == prefix or path.startswith(prefix.rstrip("/") + "/"))
        for prefix in prefixes
    )


def _reject_unknown_fields(value: dict[str, Any], schema: str, location: str, errors: list[str]) -> None:
    allowed = ALLOWED_FIELDS[schema]
    for key in value:
        if key not in allowed:
            errors.append(f"{location}.{key!r}: unknown field for {schema} object")


def validate_dataset(
    dataset: Any,
    source_roots: dict[str, Path | str],
    *,
    check_revisions: bool = True,
) -> list[str]:
    """Return contract violations; does not use a model, database, or product code."""
    errors: list[str] = []
    if not _is_mapping(dataset):
        return ["dataset: expected a JSON object"]

    _reject_unknown_fields(dataset, "dataset", "dataset", errors)
    if dataset.get("schema_version") != 1:
        errors.append("dataset.schema_version: expected 1")
    if dataset.get("status") != "draft_for_human_confirmation":
        errors.append("dataset.status: must be draft_for_human_confirmation")
    if dataset.get("hash_mode") != HASH_MODE:
        errors.append(f"dataset.hash_mode: expected {HASH_MODE!r}")

    repositories = dataset.get("repositories")
    if not isinstance(repositories, list):
        errors.append("dataset.repositories: expected a list")
        repositories = []
    repository_by_id: dict[str, dict[str, Any]] = {}
    for index, repository in enumerate(repositories):
        location = f"dataset.repositories[{index}]"
        if not _is_mapping(repository):
            errors.append(f"{location}: expected an object")
            continue
        _reject_unknown_fields(repository, "repository", location, errors)
        repo_id = repository.get("id")
        if not isinstance(repo_id, str) or not repo_id:
            errors.append(f"{location}.id: expected a non-empty string")
            continue
        if repo_id in repository_by_id:
            errors.append(f"{location}.id: duplicate repository id {repo_id!r}")
        repository_by_id[repo_id] = repository
        if repository.get("kind") not in VALID_KINDS:
            errors.append(f"{location}.kind: expected one of {sorted(VALID_KINDS)}")
        commit = repository.get("commit")
        if not isinstance(commit, str) or not COMMIT_RE.fullmatch(commit):
            errors.append(f"{location}.commit: expected a full lowercase Git commit hash")
        prefixes = repository.get("path_prefixes", [])
        if not isinstance(prefixes, list) or any(not isinstance(item, str) or not item for item in prefixes):
            errors.append(f"{location}.path_prefixes: expected a list of non-empty strings")
        if repo_id not in source_roots:
            errors.append(f"source root missing for repository {repo_id!r}")
        elif check_revisions and repository.get("verify_current_revision", True):
            root = Path(source_roots[repo_id]).resolve()
            try:
                result = subprocess.run(
                    ["git", "-C", str(root), "rev-parse", "HEAD"],
                    check=True,
                    capture_output=True,
                    text=True,
                    timeout=10,
                )
                actual = result.stdout.strip().lower()
                if isinstance(commit, str) and actual != commit:
                    errors.append(
                        f"repository {repo_id!r}: checked-out commit {actual!r} does not match pinned {commit!r}"
                    )
            except (OSError, subprocess.SubprocessError) as exc:
                errors.append(f"repository {repo_id!r}: cannot verify Git revision ({exc})")

    questions = dataset.get("questions")
    if not isinstance(questions, list):
        errors.append("dataset.questions: expected a list")
        questions = []
    if len(questions) != sum(EXPECTED_COUNTS.values()):
        errors.append(f"dataset.questions: expected {sum(EXPECTED_COUNTS.values())} questions, got {len(questions)}")

    counts = {category: 0 for category in EXPECTED_COUNTS}
    question_ids: set[str] = set()
    for index, question in enumerate(questions):
        location = f"dataset.questions[{index}]"
        if not _is_mapping(question):
            errors.append(f"{location}: expected an object")
            continue
        _reject_unknown_fields(question, "question", location, errors)
        question_id = question.get("id")
        if not isinstance(question_id, str) or not question_id:
            errors.append(f"{location}.id: expected a non-empty string")
        elif question_id in question_ids:
            errors.append(f"{location}.id: duplicate question id {question_id!r}")
        else:
            question_ids.add(question_id)

        category = question.get("category")
        if category not in EXPECTED_COUNTS:
            errors.append(f"{location}.category: unsupported category {category!r}")
        else:
            counts[category] += 1

        if not isinstance(question.get("question"), str) or len(question["question"].strip()) < 12:
            errors.append(f"{location}.question: expected a non-empty representative question")

        repo_id = question.get("repository")
        repository = repository_by_id.get(repo_id) if isinstance(repo_id, str) else None
        if repository is None:
            errors.append(f"{location}.repository: unknown repository {repo_id!r}")
        elif question.get("source_kind") != repository.get("kind"):
            errors.append(f"{location}.source_kind: does not match repository metadata")

        evidence_items = question.get("evidence")
        if not isinstance(evidence_items, list) or not evidence_items:
            errors.append(f"{location}.evidence: expected at least one source locator")
            continue
        for evidence_index, evidence in enumerate(evidence_items):
            evidence_location = f"{location}.evidence[{evidence_index}]"
            if not _is_mapping(evidence):
                errors.append(f"{evidence_location}: expected an object")
                continue
            _reject_unknown_fields(evidence, "evidence", evidence_location, errors)
            path = evidence.get("path")
            if not _safe_relative_path(path):
                errors.append(f"{evidence_location}.path: expected a safe repository-relative path")
                continue
            evidence_repo_id = evidence.get("repository", repo_id)
            evidence_repository = (
                repository_by_id.get(evidence_repo_id) if isinstance(evidence_repo_id, str) else None
            )
            if evidence_repository is None:
                errors.append(f"{evidence_location}.repository: unknown repository {evidence_repo_id!r}")
                continue
            if evidence_repository.get("kind") != question.get("source_kind"):
                errors.append(f"{evidence_location}.repository: kind differs from the question source_kind")
            if evidence_repo_id not in source_roots:
                errors.append(f"source root missing for evidence repository {evidence_repo_id!r}")
                continue
            if not _within_prefix(path, evidence_repository.get("path_prefixes", [])):
                errors.append(f"{evidence_location}.path: outside declared path scope")
                continue

            root = Path(source_roots[evidence_repo_id]).resolve()
            source_path = (root / Path(*path.split("/"))).resolve()
            try:
                source_path.relative_to(root)
            except ValueError:
                errors.append(f"{evidence_location}.path: resolves outside the repository root")
                continue
            try:
                raw = source_path.read_bytes()
            except OSError as exc:
                errors.append(f"{evidence_location}.path: cannot read source file ({exc})")
                continue

            try:
                canonical_raw = raw.replace(b"\r\n", b"\n").replace(b"\r", b"\n")
                canonical_text = canonical_raw.decode("utf-8")
            except UnicodeDecodeError:
                errors.append(f"{evidence_location}.path: source file is not valid UTF-8")
                continue

            expected_hash = evidence.get("sha256")
            if not isinstance(expected_hash, str) or not SHA256_RE.fullmatch(expected_hash):
                errors.append(f"{evidence_location}.sha256: expected a lowercase SHA-256 digest")
            elif hashlib.sha256(canonical_raw).hexdigest() != expected_hash:
                errors.append(f"{evidence_location}.sha256: hash does not match source file")

            start_line = evidence.get("start_line")
            end_line = evidence.get("end_line")
            lines = canonical_text.splitlines()
            if (
                not isinstance(start_line, int)
                or isinstance(start_line, bool)
                or not isinstance(end_line, int)
                or isinstance(end_line, bool)
                or start_line < 1
                or end_line < start_line
                or end_line > len(lines)
            ):
                errors.append(f"{evidence_location}: invalid 1-based line range")
                continue

            symbol = evidence.get("symbol")
            span = "\n".join(lines[start_line - 1 : end_line])
            if not isinstance(symbol, str) or not symbol.strip() or symbol not in span:
                errors.append(f"{evidence_location}.symbol: locator text is not present in the cited line range")

            rationale = evidence.get("rationale")
            if not isinstance(rationale, str) or not rationale.strip() or len(rationale) > 280 or "\n" in rationale:
                errors.append(f"{evidence_location}.rationale: expected one concise non-source sentence")

    for category, expected in EXPECTED_COUNTS.items():
        if counts[category] != expected:
            errors.append(f"category {category!r} count: expected {expected}, got {counts[category]}")
    return errors


def _parse_source_root(value: str) -> tuple[str, Path]:
    repo_id, separator, raw_path = value.partition("=")
    if not separator or not repo_id or not raw_path:
        raise argparse.ArgumentTypeError("source root must use REPOSITORY_ID=PATH")
    return repo_id, Path(raw_path)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("dataset", type=Path, help="JSON representative-question dataset")
    parser.add_argument(
        "--source-root",
        action="append",
        type=_parse_source_root,
        default=[],
        metavar="REPOSITORY_ID=PATH",
        help="root of each pinned source repository; repeat for every dataset repository",
    )
    parser.add_argument(
        "--no-revision-check",
        action="store_true",
        help="skip checking each repository's current Git HEAD against its pinned commit",
    )
    args = parser.parse_args(argv)
    roots = dict(args.source_root)
    if len(roots) != len(args.source_root):
        parser.error("each repository ID may be supplied only once")
    try:
        dataset = json.loads(args.dataset.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"dataset: cannot read valid JSON ({exc})", file=sys.stderr)
        return 2

    errors = validate_dataset(dataset, roots, check_revisions=not args.no_revision_check)
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1

    counts = ", ".join(f"{name}={count}" for name, count in EXPECTED_COUNTS.items())
    print(f"Validated 30 draft questions; {counts}; source hashes and locators match.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
