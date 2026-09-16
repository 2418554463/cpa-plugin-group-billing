#!/usr/bin/env python3
"""Real CPA + built shared library, isolated SQLite and dummy-only upstreams.

Usage: python3 scripts/e2e_group_billing.py --host /path/cli-proxy-api --plugin /path/cpa-key-billing.so
No existing config/database is accepted. The clock-boundary fixture changes only
the new temporary DB while CPA is stopped; production never offers immediate binds.
"""
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
BASE = "/v0/management/plugins/cpa-key-billing"
SELF = "/v0/resource/plugins/cpa-key-billing"


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class FakeKeeper(BaseHTTPRequestHandler):
    alias = "Keeper 初始备注"

    def log_message(self, *_):
        pass

    def request(self):
        assert not self.headers.get("Authorization"), "CPA key leaked to Keeper"
        assert self.headers.get("X-CPA-Usage-Keeper-Request") == "fetch"
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or "{}")
        if self.path == "/api/v1/auth/login":
            assert body == {"password": "dummy-keeper-password"}
            self.send_response(200)
            self.send_header("Set-Cookie", "cpa_usage_keeper_session=dummy; Path=/; HttpOnly")
            data = {}
        elif "cpa_usage_keeper_session=dummy" not in self.headers.get("Cookie", ""):
            self.send_response(401)
            data = {}
        elif self.path == "/api/v1/usage/api-keys/settings":
            self.send_response(200)
            data = {"items": [{"id": "21", "apiKey": "e2e-downstream-key", "keyAlias": type(self).alias}]}
        elif self.path == "/api/v1/usage/identities":
            self.send_response(200)
            data = {"identities": []}
        elif self.path == "/api/v1/usage/api-keys/21" and self.command == "PATCH":
            type(self).alias = body["keyAlias"]
            self.send_response(200)
            data = {"id": "21", "keyAlias": type(self).alias}
        else:
            self.send_response(404)
            data = {}
        raw = json.dumps(data, ensure_ascii=False).encode()
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    do_GET = request
    do_POST = request
    do_PATCH = request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True, type=Path)
    parser.add_argument("--plugin", required=True, type=Path)
    args = parser.parse_args()
    host_path, plugin_path = args.host.resolve(), args.plugin.resolve()
    assert host_path.is_file() and plugin_path.is_file()
    with tempfile.TemporaryDirectory(prefix="cpa-group-e2e-") as directory:
        runtime = Path(directory)
        (runtime / "plugins").mkdir()
        (runtime / "auth").mkdir()
        shutil.copy2(plugin_path, runtime / "plugins" / ("cpa-key-billing" + plugin_path.suffix))
        host_port, upstream_port = port(), port()
        keeper = ThreadingHTTPServer(("127.0.0.1", 0), FakeKeeper)
        keeper_thread = threading.Thread(target=keeper.serve_forever, daemon=True)
        keeper_thread.start()
        config = (ROOT / "scripts/e2e_config.yaml").read_text()
        for token, value in {"__PORT__": str(host_port), "__RUNTIME_DIR__": str(runtime), "__UPSTREAM_ORIGIN__": f"http://127.0.0.1:{upstream_port}", "__UPSTREAM_API_KEY__": '"dummy-upstream-key"'}.items():
            config = config.replace(token, value)
        config = config.replace('      debug: true', f'      debug: true\n      keeper_url: "http://127.0.0.1:{keeper.server_port}"\n      keeper_password_env: "CPA_GROUP_TEST_KEEPER_PASSWORD"')
        (runtime / "config.yaml").write_text(config)
        os.chmod(runtime / "config.yaml", 0o600)
        env = dict(os.environ, CPA_GROUP_TEST_KEEPER_PASSWORD="dummy-keeper-password")
        upstream_log = (runtime / "upstream.log").open("w")
        upstream = subprocess.Popen([sys.executable, str(ROOT / "scripts/dummy_provider.py"), "--host", "127.0.0.1", "--port", str(upstream_port)], stdout=upstream_log, stderr=subprocess.STDOUT)
        process = None
        host_log = (runtime / "host.log").open("a")

        def api(method, path, data=None, key="e2e-management-key", status=200):
            raw = json.dumps(data).encode() if data is not None else None
            request = Request(f"http://127.0.0.1:{host_port}" + path, data=raw, method=method, headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
            try:
                response = urlopen(request, timeout=30)
            except HTTPError as error:
                response = error
            with response:
                payload = response.read()
                assert response.status == status, (method, path, response.status, payload[:1200])
                return json.loads(payload)

        def management(method, path, data=None, status=200):
            return api(method, BASE + path, data, status=status)

        def wait(check, message, seconds=15):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                try:
                    value = check()
                    if value:
                        return value
                except (URLError, AssertionError):
                    pass
                time.sleep(0.05)
            raise AssertionError(message)

        def start():
            nonlocal process
            process = subprocess.Popen([str(host_path), "-config", str(runtime / "config.yaml"), "-local-model"], stdout=host_log, stderr=subprocess.STDOUT, env=env)
            wait(lambda: management("GET", "/v1/groups"), "CPA did not start", 60)

        def stop():
            nonlocal process
            if process is not None:
                process.terminate()
                process.wait(timeout=15)
                process = None

        def model_request(model, prompt="Reply OK", status=200):
            return api("POST", "/v1/chat/completions", {"model": model, "messages": [{"role": "user", "content": prompt}], "max_tokens": 128}, key="e2e-downstream-key", status=status)

        try:
            start()
            management("POST", "/keys/sync", {"keys": ["e2e-downstream-key"], "allow_empty": False})
            keys = management("GET", "/keys")["keys"]
            scope = keys[0]["scope"]
            assert keys[0]["label"] == FakeKeeper.alias
            management("POST", "/keys/label", {"scope": scope, "label": "从计费编辑"})
            assert FakeKeeper.alias == "从计费编辑"
            FakeKeeper.alias = "从 Keeper 编辑"
            management("POST", "/v1/integrations/keeper/refresh", {})
            assert management("GET", "/keys")["keys"][0]["label"] == FakeKeeper.alias
            management("PATCH", "/v1/shared-labels", {"subject_kind": "downstream_key", "subject_id": scope, "value": "冲突写入", "expected_value": "旧备注"}, status=409)
            print("PASS Keeper API 双向备注、冲突、会话与真实 SQLite 缓存", flush=True)

            models = ["gpt-auto", "e2e-chat-to-chat-nonstream", "e2e-credential-route"]
            for model in models:
                management("PUT", "/prices", {"model_id": model, "input_per_1m": 1, "output_per_1m": 2, "cache_read_per_1m": 0.1, "cache_write_per_1m": 1.25})
            groups = []
            for index, model in enumerate(models):
                pool = {"mode": "inherit"}
                if index == 2:
                    pool = {"mode": "selected", "allow_classes": [{"source": "ai-providers", "provider": "openai-compatible-route-allowed-e2e"}]}
                group = management("POST", "/v1/groups", {"name": f"动态资源 {index}", "models": [model], "pool": pool})
                group = management("POST", "/v1/groups/publish", {"group_id": group["id"], "expected_revision": group["revision"], "reason": "E2E publish"})
                groups.append(group)
            policies = [{"group_id": g["id"], "enabled": True, "daily_limit_usd": "0.000001" if i == 0 else None, "concurrency_limit": 2 if i == 0 else None} for i, g in enumerate(groups)]
            plan = management("POST", "/v1/plans", {"name": "动态计划", "policies": policies})
            binding = management("PUT", "/v1/key-plan-bindings", {"scope": scope, "mode": "grouped", "plan_id": plan["id"], "expected_revision": 0, "effective": "next_day", "reason": "E2E schedule"})
            assert binding["active"] is None and binding["pending"]["mode"] == "grouped"
            stop()
            # Time-boundary fixture, stopped host, new disposable DB only.
            with sqlite3.connect(runtime / "state.db") as db:
                db.execute("UPDATE gb_key_bindings SET effective_from_ns=?", (time.time_ns() - 1_000_000_000,))
            start()

            def usage():
                return management("GET", "/v1/keys/group-usage?scope=" + scope)

            def group_usage(index):
                return next(v for v in usage()["groups"] if v["group_id"] == groups[index]["id"])

            model_request(models[0] + "(high)")
            wait(lambda: group_usage(0)["requests"] == 1, "Alias usage was not grouped")
            error = model_request(models[0], status=429)
            assert "group_quota_exceeded" in json.dumps(error)
            model_request(models[1])
            wait(lambda: group_usage(1)["requests"] == 1, "Other group affected")
            event = management("GET", "/events?group_id=" + groups[0]["id"])["entries"][0]
            assert event["group"]["group_id"] == groups[0]["id"] and event["upstream_model"] != models[0]
            assert group_usage(0)["accounting_complete"]
            print("PASS 实际别名/思考后缀归组、额度阻断与组间独立", flush=True)

            management("POST", "/v1/keys/group-reset", {"scope": scope, "group_id": groups[0]["id"], "reason": "E2E reset"})
            assert group_usage(0)["used_usd"] == "0"
            assert group_usage(1)["used_usd"] != "0"
            policies[0]["daily_limit_usd"] = "100"
            management("PUT", "/v1/plans", {"plan_id": plan["id"], "expected_revision": plan["revision"], "name": plan["name"], "policies": policies, "reason": "E2E concurrency"})
            with concurrent.futures.ThreadPoolExecutor(2) as executor:
                futures = [executor.submit(model_request, models[0], "E2E HOLD CONCURRENCY SLOT") for _ in range(2)]
                wait(lambda: group_usage(0)["active_requests"] == 2, "Two group slots not acquired", 2)
                blocked = model_request(models[0], status=429)
                assert "group_concurrency_exceeded" in json.dumps(blocked)
                model_request(models[1])
                for future in futures:
                    future.result()
            wait(lambda: group_usage(0)["active_requests"] == 0 and group_usage(0)["requests"] == 3, "Group slots/usage not settled")
            print("PASS 分组并发 2、完成释放、单组重置", flush=True)

            model_request(models[2])
            wait(lambda: group_usage(2)["requests"] == 1, "Provider group usage missing")
            event = management("GET", "/events?group_id=" + groups[2]["id"])["entries"][0]
            assert event["provider"] == "openai-compatible-route-allowed-e2e"
            management("PUT", "/keys/routes", {"scope": scope, "bindings": {"denied_credential_providers": [{"source": "ai-providers", "provider": "openai-compatible-route-allowed-e2e"}]}})
            model_request(models[2], status=503)
            management("PUT", "/keys/routes", {"scope": scope, "bindings": {}})
            self_view = api("GET", SELF + "/v1/subscription", key="e2e-downstream-key")
            assert len(self_view["groups"]) == 3
            api("GET", SELF + "/v1/subscription?scope=other", key="e2e-downstream-key", status=400)
            before = group_usage(0)["used_usd"]
            stop(); start()
            assert group_usage(0)["used_usd"] == before and group_usage(0)["active_requests"] == 0
            with sqlite3.connect(runtime / "state.db") as db:
                assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                assert not db.execute("PRAGMA foreign_key_check").fetchall()
                cached = json.dumps(db.execute("SELECT * FROM gb_shared_labels").fetchall())
                assert "e2e-downstream-key" not in cached and "dummy-keeper-password" not in cached
            print("PASS 供应商分组与原权限取交集、自助权限隔离、重启持久化及数据库完整性", flush=True)
            print("GROUP E2E PASSED", flush=True)
        except BaseException:
            host_log.flush()
            log = (runtime / "host.log").read_text()
            print(log[:6000] + "\n...\n" + log[-6000:], file=sys.stderr)
            raise
        finally:
            stop()
            upstream.terminate(); upstream.wait(timeout=10)
            keeper.shutdown(); keeper.server_close(); keeper_thread.join(timeout=5)
            host_log.close(); upstream_log.close()


if __name__ == "__main__":
    main()
