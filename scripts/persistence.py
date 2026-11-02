#!/usr/bin/env python3
"""Restart persistence checks for teemoHub.

Snapshots the wallet, ledger and request history, restarts the service with
`make restart`, and asserts nothing moved. Then proves the restarted process
can still admit and bill a fresh request from persisted state.

State it creates: one short-lived key (revoked at the end) and one tiny request.
Usage: python3 scripts/persistence.py [--report runtime/persistence-report.json]
"""

from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

CONTROL = "http://127.0.0.1:18080"
GATEWAY = "http://127.0.0.1:18081/v1/chat/completions"
ADMIN = ("admin", "TeemoAdmin!2026")
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class Control:
    def __init__(self) -> None:
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar)
        )

    def call(self, path: str, method: str = "GET", body: dict | None = None) -> tuple[int, dict]:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            CONTROL + path,
            data=data,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        try:
            with self.opener.open(req, timeout=30) as resp:
                return resp.status, json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                payload = json.loads(raw or b"{}")
            except json.JSONDecodeError:
                payload = {"raw": raw.decode(errors="replace")}
            return e.code, payload

    def login(self) -> None:
        status, payload = self.call(
            "/api/auth/login", "POST", {"username": ADMIN[0], "password": ADMIN[1]}
        )
        if status != 200:
            raise RuntimeError(f"login failed: {status} {payload}")

    def wallet(self) -> dict:
        status, payload = self.call("/api/wallet")
        if status != 200:
            raise RuntimeError(f"wallet failed: {status} {payload}")
        return payload

    def requests(self, limit: int = 5) -> dict:
        status, payload = self.call(f"/api/requests?limit={limit}")
        if status != 200:
            raise RuntimeError(f"requests failed: {status} {payload}")
        return payload

    def request(self, rid: str) -> dict:
        status, payload = self.call(f"/api/requests/{rid}")
        if status != 200:
            raise RuntimeError(f"request {rid}: {status} {payload}")
        return payload

    def wait_settled(self, rid: str, timeout: float = 60.0) -> dict:
        deadline = time.time() + timeout
        last = None
        while time.time() < deadline:
            payload = self.request(rid)
            last = payload
            if payload["request"]["status"] not in ("reserved", "streaming"):
                return payload
            time.sleep(0.2)
        raise RuntimeError(f"{rid} did not settle: {json.dumps(last)[:400]}")


class Report:
    def __init__(self) -> None:
        self.rows: list[dict] = []
        self.failures = 0

    def check(self, name: str, ok: bool, detail: str = "") -> None:
        self.rows.append({"case": name, "ok": ok, "detail": detail})
        if not ok:
            self.failures += 1
        print(("PASS " if ok else "FAIL ") + name + ("  " + detail if detail else ""))

    def dump(self, path: str) -> None:
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        with open(path, "w") as f:
            json.dump({"rows": self.rows, "failures": self.failures}, f, indent=2)
            f.write("\n")


def snapshot(ctl: Control) -> dict:
    wallet = ctl.wallet()
    requests = ctl.requests(limit=5)
    entries = [
        (e["id"], e["entry_type"], e["amount_nano"], e["balance_after_nano"])
        for e in wallet.get("entries") or []
    ]
    newest = [
        (r["id"], r["status"], r["cost_nano"], r["created_at"])
        for r in requests.get("items") or []
    ]
    newest.sort()
    return {
        "balance_nano": int(wallet["balance_nano"]),
        "entries": entries,
        "requests_total": int(requests["total"]),
        "newest": newest,
    }


def wait_healthy(timeout: float = 90.0) -> dict | None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(CONTROL + "/healthz", timeout=5) as resp:
                payload = json.loads(resp.read() or b"{}")
                if resp.status == 200 and payload.get("ok"):
                    return payload
        except (urllib.error.URLError, ConnectionError, OSError, ValueError):
            pass
        time.sleep(0.5)
    return None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", default="runtime/persistence-report.json")
    args = ap.parse_args()

    ctl = Control()
    ctl.login()
    report = Report()
    before = snapshot(ctl)
    report.check(
        "pre-restart snapshot captured",
        before["requests_total"] > 0 and before["balance_nano"] != 0,
        f"balance={before['balance_nano']} requests={before['requests_total']} entries={len(before['entries'])}",
    )

    proc = subprocess.run(
        ["make", "restart"], cwd=ROOT, capture_output=True, text=True, timeout=180
    )
    report.check(
        "make restart exits 0",
        proc.returncode == 0,
        (proc.stdout.strip().splitlines() or [""])[-1] if proc.returncode == 0 else proc.stderr[-300:],
    )
    health = wait_healthy()
    report.check(
        "healthz is ok after restart",
        health is not None,
        "" if health is None else "checks=" + ",".join(f"{c['name']}:{'ok' if c['ok'] else 'down'}" for c in health["checks"]),
    )

    ctl2 = Control()
    ctl2.login()
    after = snapshot(ctl2)
    report.check(
        "wallet balance survives restart",
        after["balance_nano"] == before["balance_nano"],
        f"{before['balance_nano']} -> {after['balance_nano']}",
    )
    report.check(
        "ledger entries survive restart",
        after["entries"] == before["entries"],
        f"{len(after['entries'])} entries identical",
    )
    report.check(
        "request history survives restart",
        after["requests_total"] == before["requests_total"] and after["newest"] == before["newest"],
        f"total {before['requests_total']} -> {after['requests_total']}",
    )

    key_id = 0
    try:
        status, payload = ctl2.call(
            "/api/keys", "POST", {"name": f"persist-{int(time.time())}", "rpm": 60}
        )
        if status != 200:
            raise RuntimeError(f"create key failed: {status} {payload}")
        key_id = int(payload["key"]["id"])
        key = payload["secret"]

        body = {
            "model": "deepseek-chat",
            "max_tokens": 8,
            "messages": [{"role": "user", "content": "Reply with exactly: persist-ok"}],
        }
        req = urllib.request.Request(
            GATEWAY,
            data=json.dumps(body).encode(),
            headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                rid = resp.getheader("X-TeemoHub-Request-Id") or ""
                resp.read()
        except urllib.error.HTTPError as e:
            rid = e.headers.get("X-TeemoHub-Request-Id") or ""
            raise RuntimeError(f"post-restart request failed: {e.code} {e.read()[:200]}")

        detail = ctl2.wait_settled(rid)
        r = detail["request"]
        ledger_net = sum(int(e["amount_nano"]) for e in detail.get("ledger") or [])
        events = [e for e in detail.get("usage_events") or [] if e.get("final") == 1]
        report.check(
            "restarted process bills a new request",
            r["status"] == "completed" and r["cost_nano"] > 0 and ledger_net == -r["cost_nano"] and len(events) == 1,
            f"status={r['status']} cost={r['cost_nano']} ledger={ledger_net} finals={len(events)}",
        )
        final = snapshot(ctl2)
        report.check(
            "only the new request moved the wallet",
            final["requests_total"] == after["requests_total"] + 1
            and final["balance_nano"] == after["balance_nano"] - r["cost_nano"],
            f"+1 request, debit={after['balance_nano'] - final['balance_nano']} cost={r['cost_nano']}",
        )
    finally:
        if key_id:
            try:
                ctl2.call(f"/api/keys/{key_id}", "DELETE")
            except Exception:
                pass

    report.dump(args.report)
    print(f"\n{len(report.rows) - report.failures}/{len(report.rows)} checks passed; report -> {args.report}")
    return 1 if report.failures else 0


if __name__ == "__main__":
    sys.exit(main())
