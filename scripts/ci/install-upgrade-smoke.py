"""Local-only smoke: fresh setup and replacement of the pre-change binary.

Uses two disposable CI databases. Never configures an LLM or starts a scan.
This verifies the pinned baseline upgrade, not every historical release or the
online updater/download path.
"""
import contextlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:18787"
PASSWORD = "ci-smoke-password-only"
LOGS = Path("ci-smoke-logs").resolve()
LOGS.mkdir(exist_ok=True)
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(path, body=None, token=None, expected=200):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, headers=headers,
                                 data=None if body is None else json.dumps(body).encode())
    try:
        response = opener.open(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == expected, (path, response.status, raw[:300])
        if "application/json" in response.headers.get("Content-Type", ""):
            return json.loads(raw)
        return raw


@contextlib.contextmanager
def running(binary, folder, database, label):
    target = folder / "artex"
    shutil.copy2(binary, target)
    target.chmod(0o755)
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(("ANTHROPIC_", "OPENAI_", "ARTEX_LLM_"))}
    env["ARTEX_PG_DSN"] = "postgres://artex:ci-only-password@127.0.0.1:5432/" + database + "?sslmode=disable"
    env["ARTEX_CONFIG"] = str(folder / "config.json")
    with (LOGS / (label + ".log")).open("w") as log:
        proc = subprocess.Popen([str(target), "-addr", "127.0.0.1:18787", "-proxy", ""],
                                cwd=folder, env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            for _ in range(120):
                if proc.poll() is not None:
                    raise RuntimeError(f"{label} exited with {proc.returncode}; see {log.name}")
                try:
                    assert request("/api/health")["ok"] is True
                    break
                except (OSError, AssertionError):
                    time.sleep(0.5)
            else:
                raise RuntimeError(f"{label} did not become ready")
            yield
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise RuntimeError(f"{label} failed graceful shutdown")


def initialize():
    assert request("/api/auth/status")["initialized"] is False
    assert b"<html" in request("/").lower()
    request("/api/tasks", expected=401)
    token = request("/api/auth/init", {"password": PASSWORD})["token"]
    request("/api/auth/init", {"password": PASSWORD}, expected=403)
    request("/api/tasks", token=token)
    return token


baseline, current = [Path(p).resolve() for p in sys.argv[1:]]
with tempfile.TemporaryDirectory(prefix="artex-smoke-") as tmp:
    root = Path(tmp)
    fresh, upgrade = root / "fresh", root / "upgrade"
    fresh.mkdir()
    upgrade.mkdir()
    with running(current, fresh, "artex_fresh", "fresh"):
        initialize()
    with running(baseline, upgrade, "artex_upgrade", "baseline"):
        token = initialize()
        task = request("/api/tasks", {"description": "CI persistence sentinel", "goal": "", "seed_first_intent": False},
                       token, expected=201)
        task_id = str(task["id"])
    key_before = (upgrade / "jwt.key").read_bytes()
    with running(current, upgrade, "artex_upgrade", "upgraded"):
        assert request("/api/auth/status")["initialized"] is True
        new_token = request("/api/auth/login", {"username": "ARTEX", "password": PASSWORD})["token"]
        saved = request("/api/tasks/" + task_id, token=new_token)
        assert str(saved["id"]) == task_id
        request("/api/tasks", token=token)
        assert (upgrade / "jwt.key").read_bytes() == key_before
    print("PASS: fresh install; baseline-to-current password, task and JWT-key preservation.")
