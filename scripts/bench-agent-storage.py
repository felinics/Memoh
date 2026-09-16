#!/usr/bin/env python3
"""Measure Codex initialization in isolated, explicitly selected filesystems.

No credentials or configuration are copied from the caller. A fresh Home per
trial measures first initialization; subsequent samples reuse that Home. This
does not flush the OS page cache and must not be described as a cold-disk test.
Run inside a disposable workspace to compare its real mounts, not on the host.
"""

import argparse
import datetime
import json
import math
import os
from pathlib import Path
import platform
import selectors
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid


MAX_LINE = 4 * 1024 * 1024


class BenchmarkError(Exception):
    pass


class RpcProcess:
    """A bounded JSONL reader which accepts only the requested response ID."""

    def __init__(self, command, cwd, env, stderr_path):
        self.stderr = open(stderr_path, "wb")
        self.started_ns = time.monotonic_ns()
        try:
            self.process = subprocess.Popen(
                command, cwd=cwd, env=env, stdin=subprocess.PIPE,
                stdout=subprocess.PIPE, stderr=self.stderr, start_new_session=True,
            )
        except BaseException:
            self.stderr.close()
            raise
        self.selector = selectors.DefaultSelector()
        self.selector.register(self.process.stdout, selectors.EVENT_READ)
        self.buffer = b""
        self.ignored_messages = 0

    def send(self, message):
        try:
            self.process.stdin.write(json.dumps(message).encode() + b"\n")
            self.process.stdin.flush()
        except (BrokenPipeError, OSError) as exc:
            raise BenchmarkError("process_closed_stdin") from exc

    def request(self, method, params, timeout, started_ns=None):
        request_id = str(uuid.uuid4())
        started_ns = started_ns or time.monotonic_ns()
        deadline = started_ns + int(timeout * 1_000_000_000)
        self.send({"id": request_id, "method": method, "params": params})
        while True:
            while b"\n" in self.buffer:
                line, self.buffer = self.buffer.split(b"\n", 1)
                if len(line) > MAX_LINE:
                    raise BenchmarkError("response_line_too_large")
                try:
                    message = json.loads(line)
                except (ValueError, UnicodeError) as exc:
                    raise BenchmarkError("invalid_json_output") from exc
                if not isinstance(message, dict):
                    raise BenchmarkError("invalid_rpc_message")
                if message.get("id") != request_id or "method" in message:
                    self.ignored_messages += 1
                    continue
                if "error" in message:
                    # Keep only the protocol code: error text may include paths or secrets.
                    error = message["error"]
                    code = error.get("code") if isinstance(error, dict) else None
                    raise BenchmarkError("rpc_error:" + str(code))
                if "result" not in message:
                    raise BenchmarkError("response_missing_result")
                elapsed = (time.monotonic_ns() - started_ns) / 1_000_000_000
                if time.monotonic_ns() > deadline:
                    raise BenchmarkError("timeout")
                return message["result"], elapsed
            if len(self.buffer) > MAX_LINE:
                raise BenchmarkError("response_line_too_large")
            remaining = (deadline - time.monotonic_ns()) / 1_000_000_000
            if remaining <= 0:
                raise BenchmarkError("timeout")
            if not self.selector.select(remaining):
                raise BenchmarkError("timeout")
            chunk = os.read(self.process.stdout.fileno(), 65536)
            if not chunk:
                raise BenchmarkError("process_exited_before_response")
            self.buffer += chunk

    def close(self):
        start = time.monotonic_ns()
        self.selector.close()
        try:
            self.process.stdin.close()
        except OSError:
            pass
        forced = False
        try:
            self.process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            forced = True
            try:
                os.killpg(self.process.pid, signal.SIGTERM)
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=2)
            except ProcessLookupError:
                self.process.wait(timeout=2)
        finally:
            # The app-server can exit before one of its helper processes. They
            # share the new session's process group and belong only to this run.
            try:
                os.killpg(self.process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            self.process.stdout.close()
            self.stderr.close()
        return {
            "seconds": (time.monotonic_ns() - start) / 1_000_000_000,
            "returncode": self.process.returncode,
            "forced_termination": forced,
        }


def isolated_env(home, codex_home):
    # An allowlist avoids implicit API credentials, a caller CODEX_SQLITE_HOME,
    # and inherited tracing/configuration from contaminating measurements.
    env = {key: os.environ[key] for key in ("PATH", "SYSTEMROOT", "WINDIR", "LANG") if key in os.environ}
    env.update(HOME=str(home), CODEX_HOME=str(codex_home), RUST_LOG="error")
    return env


def inventory(roots):
    files = {}
    for label, root in roots.items():
        for base, directories, names in os.walk(root, followlinks=False):
            # Include symlinks to directories but never traverse outside our test.
            names += [name for name in directories if (Path(base) / name).is_symlink()]
            for name in names:
                path = Path(base) / name
                try:
                    stat = path.lstat()
                    files[label + "/" + str(path.relative_to(root))] = {
                        "size": stat.st_size, "mtime_ns": stat.st_mtime_ns,
                        "symlink": os.readlink(path) if path.is_symlink() else None,
                    }
                except FileNotFoundError:
                    pass  # A short-lived helper may disappear during enumeration.
    return files


def changed_files(before, after):
    return {path: info for path, info in after.items() if before.get(path) != info}


def mount_info(path):
    result = {"path": str(path), "device": path.stat().st_dev,
              "free_bytes": shutil.disk_usage(path).free}
    mountinfo = Path("/proc/self/mountinfo")
    if mountinfo.exists():
        candidates = []
        for line in mountinfo.read_text().splitlines():
            parts = line.split()
            mount = parts[4].replace("\\040", " ").replace("\\134", "\\")
            if path == Path(mount) or Path(mount) in path.parents:
                candidates.append((len(mount), line))
        if candidates:
            result["mountinfo"] = max(candidates)[1]
    else:
        result["mountinfo"] = None
    return result


def percentile(values, fraction):
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)] if values else None


def filesystem_probe(root, count):
    """Measure a bounded disposable metadata/write workload on the real mount."""
    directory = root / "filesystem-probe"
    directory.mkdir()
    results = {"files": count, "bytes_per_file": 4096, "fsync_each_file": True}
    content = b"x" * 4096
    start = time.monotonic_ns()
    for index in range(count):
        with (directory / str(index)).open("wb") as file:
            file.write(content)
            file.flush()
            os.fsync(file.fileno())
    results["create_fsync_seconds"] = (time.monotonic_ns() - start) / 1_000_000_000
    start = time.monotonic_ns()
    for index in range(count):
        (directory / str(index)).stat()
    results["stat_seconds"] = (time.monotonic_ns() - start) / 1_000_000_000
    start = time.monotonic_ns()
    for index in range(count):
        (directory / str(index)).unlink()
    directory.rmdir()
    results["delete_seconds"] = (time.monotonic_ns() - start) / 1_000_000_000
    return results


def measure(command, cwd, env, roots, stderr_path, timeout, probe_state=False):
    result = {"command": command, "cwd": str(cwd), "codex_home": env["CODEX_HOME"]}
    before = inventory(roots)
    rpc = None
    try:
        rpc = RpcProcess(command, cwd, env, stderr_path)
        response, elapsed = rpc.request("initialize", {
            "clientInfo": {"name": "memoh_storage_benchmark", "version": "1"},
            "capabilities": {"experimentalApi": True},
        }, timeout, rpc.started_ns)
        if not isinstance(response, dict) or not isinstance(response.get("userAgent"), str) or not response["userAgent"]:
            raise BenchmarkError("invalid_initialize_result")
        result.update(status="ok", initialize_seconds=elapsed,
                      user_agent=response["userAgent"])
        status = Path("/proc") / str(rpc.process.pid) / "status"
        if status.exists():
            result["process_memory"] = {line.split(":", 1)[0]: line.split(":", 1)[1].strip()
                for line in status.read_text().splitlines() if line.startswith(("VmRSS:", "VmHWM:"))}
        rpc.send({"method": "initialized"})
        result["files_after_initialize"] = changed_files(before, inventory(roots))
        if probe_state:
            probes = {}
            config, seconds = rpc.request("config/read", {"includeLayers": False}, timeout)
            effective = config.get("config", {})
            probes["config"] = {"seconds": seconds, "sqlite_home": effective.get("sqlite_home"),
                                "log_dir": effective.get("log_dir")}
            thread, seconds = rpc.request("thread/start", {
                "cwd": str(cwd), "approvalPolicy": "never", "sandbox": "read-only",
                "ephemeral": False,
            }, timeout)
            thread_id = thread["thread"]["id"]
            probes["thread_start"] = {"seconds": seconds, "thread_id": thread_id}
            goal, seconds = rpc.request("thread/goal/set", {
                "threadId": thread_id, "objective": "Isolated storage persistence probe",
                "status": "paused",
            }, timeout)
            probes["goal_set"] = {"seconds": seconds, "status": goal["goal"]["status"]}
            result["state_probe"] = probes
    except (BenchmarkError, OSError, ValueError, KeyError) as exc:
        result.update(status="failed", failure=str(exc))
    finally:
        if rpc is not None:
            result["ignored_messages"] = rpc.ignored_messages
            result["cleanup"] = rpc.close()
            if (result.get("status") == "ok" and result["cleanup"]["returncode"] != 0
                    and not result["cleanup"]["forced_termination"]):
                result.update(status="failed", failure="nonzero_exit_after_response")
        result["files_after_exit"] = changed_files(before, inventory(roots))
    return result


def existing_directory(value):
    path = Path(value)
    if not path.is_absolute() or not path.is_dir():
        raise argparse.ArgumentTypeError("must be an existing absolute directory")
    return path.resolve()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True, type=existing_directory,
                        help="explicit disposable workspace in which to create isolated test directories")
    parser.add_argument("--launcher", required=True, type=Path, help="absolute Codex executable path")
    parser.add_argument("--home-root", type=existing_directory, help="filesystem for test CODEX_HOME; defaults to workspace")
    parser.add_argument("--sqlite-root", type=existing_directory, help="experimental filesystem for sqlite_home")
    parser.add_argument("--log-root", type=existing_directory, help="experimental filesystem for log_dir")
    parser.add_argument("--trials", type=int, default=5)
    parser.add_argument("--warm-starts", type=int, default=1)
    parser.add_argument("--timeout", type=float, default=30)
    parser.add_argument("--probe-state", action="store_true", help="also create an unstarted thread and paused goal; no turn or model call")
    parser.add_argument("--filesystem-files", type=int, default=0,
                        help="also create/fsync/stat/delete this many 4 KiB files on workspace; 0 disables (slow on NFS)")
    parser.add_argument("--keep-artifacts", action="store_true", help="retain only this invocation's generated directories for inspection")
    parser.add_argument("--output", type=Path, help="JSON report path; defaults to stdout")
    args = parser.parse_args(argv)
    if not args.launcher.is_absolute() or not os.access(args.launcher, os.X_OK):
        parser.error("launcher must be an executable absolute path")
    if args.trials < 1 or args.warm_starts < 0 or args.filesystem_files < 0 or not math.isfinite(args.timeout) or args.timeout <= 0:
        parser.error("trials and timeout must be positive; warm-starts must be nonnegative")
    created = []

    def temporary(parent, label):
        path = Path(tempfile.mkdtemp(prefix="memoh-agent-bench-" + label + "-", dir=parent))
        created.append(path)
        return path

    report = {"schema_version": 1, "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "system": platform.platform(), "machine": platform.machine(), "cpu_count": os.cpu_count(),
              "cache_policy": "fresh Home then reused Home; OS and executable page caches are not flushed",
              "samples": []}
    try:
        work = temporary(args.workspace, "workspace")
        if args.filesystem_files:
            report["filesystem_probe"] = filesystem_probe(work, args.filesystem_files)
        report["launcher"] = str(args.launcher)
        version = subprocess.run([str(args.launcher), "--version"], cwd=work,
                                 env=isolated_env(work, work), capture_output=True, timeout=args.timeout, check=True)
        report["version"] = version.stdout.decode().strip()
        report["mounts_before"] = {label: mount_info(root) for label, root in {
            "workspace": args.workspace, "home": args.home_root or args.workspace,
            "sqlite": args.sqlite_root, "logs": args.log_root,
            "launcher": args.launcher.resolve().parent,
        }.items() if root is not None}
        for trial in range(args.trials):
            codex_home = temporary(args.home_root or args.workspace, "home")
            roots = {"codex_home": codex_home}
            cwd = work / ("trial-" + str(trial))
            cwd.mkdir()
            user_home = cwd / "user-home"
            user_home.mkdir()
            env = isolated_env(user_home, codex_home)
            command = [str(args.launcher), "app-server", "-c", "features.goals=true",
                       "-c", 'cli_auth_credentials_store="file"', "-c", "analytics.enabled=false"]
            for label, root, key in (("sqlite", args.sqlite_root, "sqlite_home"), ("logs", args.log_root, "log_dir")):
                if root is not None:
                    roots[label] = temporary(root, label)
                    command += ["-c", key + "=" + json.dumps(str(roots[label]))]
            for warm in range(args.warm_starts + 1):
                sample = measure(command, cwd, env, roots, cwd / ("stderr-" + str(warm) + ".log"),
                                 args.timeout, args.probe_state)
                sample.update(trial=trial, phase="first_home" if warm == 0 else "warm_home", warm_index=warm)
                report["samples"].append(sample)
        report["summary"] = {}
        for phase in ("first_home", "warm_home"):
            samples = [s for s in report["samples"] if s["phase"] == phase]
            values = [s["initialize_seconds"] for s in samples if s["status"] == "ok"]
            report["summary"][phase] = {"count": len(samples), "successes": len(values),
                "failures": len(samples) - len(values), "p50_seconds": percentile(values, .5),
                "p95_seconds": percentile(values, .95), "raw_seconds": values}
        report["mounts_after"] = {name: mount_info(Path(value["path"])) for name, value in report["mounts_before"].items()}
    finally:
        report["artifact_directories"] = [str(path) for path in created] if args.keep_artifacts else []
        if not args.keep_artifacts:
            for path in reversed(created):
                shutil.rmtree(path)
    output = json.dumps(report, indent=2) + "\n"
    if args.output:
        args.output.write_text(output)
    else:
        print(output, end="")
    return 0 if all(sample["status"] == "ok" for sample in report["samples"]) else 1


if __name__ == "__main__":
    sys.exit(main())
