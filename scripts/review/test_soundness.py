#!/usr/bin/env python3
import json
import os
import pathlib
import subprocess
import tempfile
import unittest


SCRIPT_DIR = pathlib.Path(__file__).parent
COVERAGE = SCRIPT_DIR / "account-coverage"
ADMISSION = SCRIPT_DIR / "admit-worker-model"


def run_json(command, expected_code=0):
    completed = subprocess.run(command, text=True, capture_output=True, check=False)
    if completed.returncode != expected_code:
        raise AssertionError(
            f"expected exit {expected_code}, got {completed.returncode}\n"
            f"stdout: {completed.stdout}\nstderr: {completed.stderr}"
        )
    return json.loads(completed.stdout)


class CoverageAccountingTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.repo = pathlib.Path(self.temporary.name)
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True)
        subprocess.run(["git", "-C", str(self.repo), "config", "user.name", "Test"], check=True)
        subprocess.run(["git", "-C", str(self.repo), "config", "user.email", "test@example.invalid"], check=True)
        source = self.repo / "service.py"
        source.write_text(
            "def alpha():\n    return 1\n\n\ndef beta():\n    return alpha()\n",
            encoding="utf-8",
        )
        (self.repo / "library.py").write_text(
            "def shared():\n    return 3\n", encoding="utf-8"
        )
        self.base = self.commit("base")
        source.write_text(
            "def alpha():\n    return 2\n\n\ndef beta():\n    value = alpha()\n    return value + 1\n",
            encoding="utf-8",
        )
        self.head = self.commit("head")
        self.blob = subprocess.run(
            ["git", "-C", str(self.repo), "rev-parse", f"{self.head}:service.py"],
            text=True,
            capture_output=True,
            check=True,
        ).stdout.strip()
        self.library_blob = subprocess.run(
            ["git", "-C", str(self.repo), "rev-parse", f"{self.head}:library.py"],
            text=True,
            capture_output=True,
            check=True,
        ).stdout.strip()
        inventory = run_json([
            str(COVERAGE), "--repo", str(self.repo), "--base", self.base,
            "--head", self.head, "--inventory",
        ])
        self.hunks = inventory["files"][0]["hunks"]

    def tearDown(self):
        self.temporary.cleanup()

    def commit(self, message):
        subprocess.run(["git", "-C", str(self.repo), "add", "."], check=True)
        subprocess.run(["git", "-C", str(self.repo), "commit", "-qm", message], check=True)
        return subprocess.run(
            ["git", "-C", str(self.repo), "rev-parse", "HEAD"],
            text=True,
            capture_output=True,
            check=True,
        ).stdout.strip()

    def write_record(self, *, hunks=None, blob=None, references=None):
        record = {
            "schema_version": 1,
            "base_commit": self.base,
            "head_commit": self.head,
            "files": [{
                "path": "service.py",
                "blob": blob or self.blob,
                "account": "read",
                "hunks": hunks if hunks is not None else [
                    {**hunk, "account": "read"} for hunk in self.hunks
                ],
            }],
            "references": references if references is not None else [{
                "kind": "definition",
                "path": "service.py",
                "commit": self.head,
                "blob": self.blob,
                "line": 1,
                "name": "alpha",
                "line_text": "def alpha():",
            }, {
                "kind": "call_site",
                "path": "service.py",
                "commit": self.head,
                "blob": self.blob,
                "line": 6,
                "name": "alpha",
                "line_text": "    value = alpha()",
            }],
        }
        path = self.repo / "record.json"
        path.write_text(json.dumps(record), encoding="utf-8")
        return path

    def account(self, record, expected_code=0):
        return run_json([
            str(COVERAGE), "--repo", str(self.repo), "--base", self.base,
            "--head", self.head, "--record", str(record),
        ], expected_code)

    def test_complete_record_is_accounted(self):
        result = self.account(self.write_record())
        self.assertEqual(result["status"], "complete")
        self.assertEqual(result["omissions"], [])

    def test_dropped_hunk_is_reported(self):
        recorded = [{**self.hunks[0], "account": "read"}]
        result = self.account(self.write_record(hunks=recorded), expected_code=1)
        self.assertEqual(result["status"], "partial")
        self.assertTrue(any(item["kind"] == "unaccounted_hunk" for item in result["omissions"]))

    def test_wrong_blob_claim_is_rejected(self):
        base_blob = subprocess.run(
            ["git", "-C", str(self.repo), "rev-parse", f"{self.base}:service.py"],
            text=True,
            capture_output=True,
            check=True,
        ).stdout.strip()
        result = self.account(self.write_record(blob=base_blob), expected_code=1)
        self.assertTrue(any(item["kind"] == "blob_mismatch" for item in result["omissions"]))

    def test_phantom_definition_is_rejected(self):
        references = [{
            "kind": "definition", "path": "service.py", "blob": self.blob,
            "commit": self.head, "line": 1, "name": "phantom",
            "line_text": "def alpha():",
        }]
        result = self.account(self.write_record(references=references), expected_code=1)
        self.assertTrue(any(item["kind"] == "reference_not_found" for item in result["omissions"]))

    def test_unchanged_reference_is_validated_at_its_commit(self):
        references = [{
            "kind": "definition", "path": "library.py", "commit": self.head,
            "blob": self.library_blob, "line": 1, "name": "shared",
            "line_text": "def shared():",
        }]
        result = self.account(self.write_record(references=references))
        self.assertEqual(result["status"], "complete")

    def test_reference_name_must_be_a_complete_identifier(self):
        references = [{
            "kind": "definition", "path": "library.py", "commit": self.head,
            "blob": self.library_blob, "line": 1, "name": "share",
            "line_text": "def shared():",
        }]
        result = self.account(self.write_record(references=references), expected_code=1)
        self.assertTrue(any(item["kind"] == "reference_not_found" for item in result["omissions"]))


class ModelAdmissionTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        self.policy = {
            "schema_version": 1,
            "unresolved_model": "stop",
            "roles": {
                "verifier": {
                    "guarantee": True,
                    "engine": "codex",
                    "model": "gpt-5.6-sol",
                    "family": "gpt",
                    "request_models": ["gpt-5.6-sol"],
                    "label_prefixes": ["verify:"],
                },
                "inventory": {
                    "guarantee": False,
                    "label_prefixes": ["inventory:"],
                },
            },
        }

    def tearDown(self):
        self.temporary.cleanup()

    def admit(self, record, *, role="verifier", counterpart="claude", unresolved="stop", expected_code=0):
        self.policy["unresolved_model"] = unresolved
        policy_path = self.root / "policy.json"
        record_path = self.root / "agent.json"
        policy_path.write_text(json.dumps(self.policy), encoding="utf-8")
        record_path.write_text(json.dumps(record), encoding="utf-8")
        command = [
            str(ADMISSION), "--policy", str(policy_path), "--role", role,
            "--record", str(record_path),
        ]
        if counterpart is not None:
            command.extend(["--counterpart-family", counterpart])
        return run_json(command, expected_code)

    @staticmethod
    def record(resolved="gpt-5.6-sol"):
        return {
            "schema_version": 2,
            "kind": "agent_record",
            "id": 7,
            "label": "verify:codex:0",
            "engine": "codex",
            "model": "gpt-5.6-sol",
            "resolved_model": resolved,
        }

    def test_cross_family_exact_pin_is_full(self):
        result = self.admit(self.record())
        self.assertEqual((result["admitted"], result["level"]), (True, "full"))

    def test_same_family_exact_pin_is_limited(self):
        result = self.admit(self.record(), counterpart="gpt")
        self.assertEqual((result["admitted"], result["level"]), (True, "limited"))

    def test_pin_mismatch_is_never_admitted(self):
        result = self.admit(self.record("gpt-5.6-other"), expected_code=1)
        self.assertEqual((result["admitted"], result["reason"]), (False, "pin_mismatch"))

    def test_alias_requested_worker_with_exact_resolved_pin_is_admitted(self):
        self.policy["roles"]["verifier"] = {
            "guarantee": True,
            "engine": "claude",
            "model": "claude-opus-4-8",
            "family": "claude",
            "request_models": ["opus", "claude-opus-4-8"],
            "label_prefixes": ["verify:"],
        }
        record = {
            "schema_version": 2, "kind": "agent_record", "id": 8,
            "label": "verify:claude:0", "engine": "claude", "model": "opus",
            "resolved_model": "claude-opus-4-8",
        }
        result = self.admit(record, counterpart="gpt")
        self.assertEqual((result["admitted"], result["level"]), (True, "full"))

    def test_alias_requested_worker_with_wrong_resolved_model_is_rejected(self):
        self.policy["roles"]["verifier"] = {
            "guarantee": True,
            "engine": "claude",
            "model": "claude-opus-4-8",
            "family": "claude",
            "request_models": ["opus"],
            "label_prefixes": ["verify:"],
        }
        record = {
            "schema_version": 2, "kind": "agent_record", "id": 8,
            "label": "verify:claude:0", "engine": "claude", "model": "opus",
            "resolved_model": "claude-fable-5",
        }
        result = self.admit(record, counterpart="gpt", expected_code=1)
        self.assertEqual((result["admitted"], result["reason"]), (False, "pin_mismatch"))

    def test_record_label_must_match_asserted_role(self):
        record = self.record()
        record["label"] = "propose:codex:0"
        result = self.admit(record, expected_code=1)
        self.assertEqual((result["admitted"], result["reason"]), (False, "role_label_mismatch"))

    def test_unresolved_model_stops_by_default(self):
        result = self.admit(self.record(None), expected_code=1)
        self.assertEqual((result["admitted"], result["reason"]), (False, "resolved_model_unavailable"))

    def test_unresolved_model_can_be_policy_limited(self):
        result = self.admit(self.record(None), unresolved="limited")
        self.assertEqual((result["admitted"], result["level"]), (True, "limited"))

    def test_mechanical_role_is_outside_guarantee(self):
        record = {
            "schema_version": 2, "kind": "agent_record", "id": 9,
            "label": "inventory:codex:0",
        }
        result = self.admit(record, role="inventory", counterpart=None)
        self.assertEqual((result["admitted"], result["level"]), (True, "mechanical"))


if __name__ == "__main__":
    unittest.main()
