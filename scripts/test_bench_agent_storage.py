"""Regression tests for measurement boundaries, without a Codex install or API key."""

import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("benchmark", Path(__file__).with_name("bench-agent-storage.py"))
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)


class RpcMeasurementTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def rpc(self, body):
        program = self.root / "fake.py"
        program.write_text("import json, os, sys\nr=json.loads(sys.stdin.readline())\n" + body)
        return benchmark.RpcProcess(
            [sys.executable, str(program)], self.root,
            benchmark.isolated_env(self.root, self.root), self.root / "stderr.log",
        )

    def test_notifications_wrong_ids_and_server_requests_are_not_success(self):
        rpc = self.rpc(
            "print(json.dumps({'method':'progress','params':{}}),flush=True)\n"
            "print(json.dumps({'id':'another-id','result':{'wrong':True}}),flush=True)\n"
            "print(json.dumps({'id':r['id'],'method':'server/request','params':{}}),flush=True)\n"
            "print(json.dumps({'id':r['id'],'result':{'matched':True}}),flush=True)\n"
        )
        try:
            result, elapsed = rpc.request("initialize", {}, 5, rpc.started_ns)
            self.assertEqual(result, {"matched": True})
            self.assertEqual(rpc.ignored_messages, 3)
            self.assertGreater(elapsed, 0)
        finally:
            rpc.close()

    def test_large_stderr_cannot_fill_a_pipe_and_block_handshake(self):
        rpc = self.rpc(
            "sys.stderr.write('diagnostic'*100000);sys.stderr.flush()\n"
            "print(json.dumps({'id':r['id'],'result':{}}),flush=True)\n"
        )
        try:
            self.assertEqual(rpc.request("initialize", {}, 5)[0], {})
        finally:
            rpc.close()
        self.assertGreater((self.root / "stderr.log").stat().st_size, 65536)

    def test_failures_are_never_counted_as_successful_latencies(self):
        cases = [
            ("print(json.dumps({'id':r['id'],'error':{'code':-1,'message':'private'}}),flush=True)\n", "rpc_error:-1"),
            ("print('not-json',flush=True)\n", "invalid_json_output"),
            ("print(json.dumps({'id':r['id']}),flush=True)\n", "response_missing_result"),
            ("sys.exit(0)\n", "process_exited_before_response"),
            ("sys.stdout.write('x'*(5*1024*1024));sys.stdout.flush()\n", "response_line_too_large"),
        ]
        for body, failure in cases:
            with self.subTest(failure=failure):
                rpc = self.rpc(body)
                try:
                    with self.assertRaisesRegex(benchmark.BenchmarkError, failure):
                        rpc.request("initialize", {}, 5)
                finally:
                    rpc.close()

    def test_no_reply_times_out_even_if_process_wrote_a_notification(self):
        rpc = self.rpc("print(json.dumps({'method':'progress'}),flush=True)\nsys.stdin.read()\n")
        try:
            with self.assertRaisesRegex(benchmark.BenchmarkError, "timeout"):
                rpc.request("initialize", {}, .3)
        finally:
            cleanup = rpc.close()
        self.assertEqual(cleanup["returncode"], 0)

    def test_clean_environment_does_not_reuse_auth_or_sqlite_location(self):
        with patch.dict(os.environ, {"OPENAI_API_KEY": "secret", "CODEX_SQLITE_HOME": "/existing-db", "HOME": "/existing-home"}):
            env = benchmark.isolated_env(self.root, self.root / "codex")
        self.assertNotIn("OPENAI_API_KEY", env)
        self.assertNotIn("CODEX_SQLITE_HOME", env)
        self.assertEqual(env["HOME"], str(self.root))

    def test_matching_id_with_invalid_initialize_payload_is_a_failed_sample(self):
        program = self.root / "fake.py"
        program.write_text(
            "import json,sys\nr=json.loads(sys.stdin.readline())\n"
            "print(json.dumps({'id':r['id'],'result':None}),flush=True)\nsys.stdin.read()\n"
        )
        sample = benchmark.measure(
            [sys.executable, str(program)], self.root,
            benchmark.isolated_env(self.root, self.root), {"home": self.root},
            self.root / "stderr.log", 5,
        )
        self.assertEqual(sample["status"], "failed")
        self.assertEqual(sample["failure"], "invalid_initialize_result")
        self.assertNotIn("initialize_seconds", sample)

    def test_inventory_does_not_follow_links_into_another_home(self):
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "auth.json").write_text("must not be read")
        home = self.root / "home"
        home.mkdir()
        (home / "external").symlink_to(outside, target_is_directory=True)
        files = benchmark.inventory({"home": home})
        self.assertEqual(list(files), ["home/external"])
        self.assertEqual(files["home/external"]["symlink"], str(outside))


if __name__ == "__main__":
    unittest.main()
