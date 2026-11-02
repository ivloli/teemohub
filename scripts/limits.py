#!/usr/bin/env python3
"""Concurrency and rate-limit checks for the teemoHub gateway.

Asserts that:
  1. four parallel streams on one key all get through, each settles exactly once,
     and the wallet debit equals the sum of their costs;
  2. a fifth concurrent request on that key is refused with 429 (maxIn=4);
  3. after the streams finish the slot is released and a new request is admitted;
  4. a key with rpm=3 accepts three requests and refuses the fourth within a minute.

State it creates: two short-lived keys (revoked at the end) and their usage.
Usage: python3 scripts/limits.py [--report runtime/limits-report.json]
"""

from __future__ import annotations

import argparse
import http.client
import http.cookiejar
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request

CONTROL = "http://127.0.0.1:18080"
GATEWAY_HOST = "127.0.0.1"
GATEWAY_PORT = 18081
ADMIN = ("admin", "TeemoAdmin!2026")


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

    def create_key(self, name: str, rpm: int) -> tuple[int, str]:
        status, payload = self.call("/api/keys", "POST", {"name": name, "rpm": rpm})
        if status != 200:
            raise RuntimeError(f"create key failed: {status} {payload}")
        return int(payload["key"]["id"]), payload["secret"]

    def revoke_key(self, key_id: int) -> None:
        self.call(f"/api/keys/{key_id}", "DELETE")

    def balance(self) -> int:
        status, payload = self.call("/api/wallet")
        if status != 200:
            raise RuntimeError(f"wallet failed: {status} {payload}")
        return int(payload["balance_nano"])

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


class StreamWorker(threading.Thread):
    """POSTs a streaming completion and reads it to completion."""

    def __init__(self, key: str, body: dict, go: threading.Event) -> None:
        super().__init__(daemon=True)
        self.key = key
        self.body = body
        self.go = go
        self.started = threading.Event()  # headers received (request admitted)
        self.rid = ""
        self.status = 0
        self.error = ""
        self.chunks = 0

    def run(self) -> None:
        self.go.wait(timeout=30)
        conn = http.client.HTTPConnection(GATEWAY_HOST, GATEWAY_PORT, timeout=120)
        try:
            conn.request(
                "POST",
                "/v1/chat/completions",
                body=json.dumps(self.body).encode(),
                headers={
                    "Authorization": "Bearer " + self.key,
                    "Content-Type": "application/json",
                    "Accept": "text/event-stream",
                },
            )
            resp = conn.getresponse()
            self.rid = resp.getheader("X-TeemoHub-Request-Id") or ""
            self.status = resp.status
            self.started.set()
            while True:
                line = resp.readline()
                if not line:
                    break
                self.chunks += 1
                if line.strip() == b"data: [DONE]":
                    break
        except Exception as exc:  # noqa: BLE001 - reported in the check detail
            self.error = f"{type(exc).__name__}: {exc}"
            self.started.set()
        finally:
            conn.close()


def post_json(key: str, body: dict) -> tuple[str, int, dict]:
    req = urllib.request.Request(
        f"http://{GATEWAY_HOST}:{GATEWAY_PORT}/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            rid = resp.getheader("X-TeemoHub-Request-Id") or ""
            return rid, resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        rid = e.headers.get("X-TeemoHub-Request-Id") or ""
        raw = e.read()
        try:
            return rid, e.code, json.loads(raw or b"{}")
        except json.JSONDecodeError:
            return rid, e.code, {"raw": raw.decode(errors="replace")}


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


def settled_cost(ctl: Control, rid: str) -> tuple[int, int, int]:
    """Returns (cost_nano, ledger_net_nano, final_usage_events)."""
    detail = ctl.wait_settled(rid)
    req = detail["request"]
    ledger = detail.get("ledger") or []
    events = detail.get("usage_events") or []
    return (
        int(req["cost_nano"]),
        sum(int(e["amount_nano"]) for e in ledger),
        len([e for e in events if e.get("final") == 1]),
    )


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", default="runtime/limits-report.json")
    args = ap.parse_args()

    ctl = Control()
    ctl.login()
    report = Report()
    stamp = int(time.time())
    keys: list[int] = []

    try:
        # --- inflight cap (maxIn=4) -------------------------------------
        burst_id, burst_key = ctl.create_key(f"limits-burst-{stamp}", 600)
        keys.append(burst_id)
        before = ctl.balance()
        go = threading.Event()
        body = {
            "model": "deepseek-chat",
            "stream": True,
            "max_tokens": 300,
            "messages": [
                {"role": "user", "content": "Write a numbered list from 1 to 100 of colors, one per line."}
            ],
        }
        workers = [StreamWorker(burst_key, body, go) for _ in range(4)]
        for w in workers:
            w.start()
        go.set()
        admitted = all(w.started.wait(timeout=30) for w in workers)
        report.check(
            "4 parallel streams admitted",
            admitted and all(w.status == 200 for w in workers),
            "statuses=" + ",".join(str(w.status) for w in workers),
        )

        fifth_rid, fifth_status, fifth_body = post_json(
            burst_key,
            {"model": "deepseek-chat", "max_tokens": 8, "messages": [{"role": "user", "content": "hi"}]},
        )
        code = (fifth_body.get("error") or {}).get("code", "")
        report.check(
            "5th concurrent request refused with 429",
            fifth_status == 429 and code == "rate_limit_exceeded",
            f"http={fifth_status} code={code} rid={fifth_rid or '-'}",
        )

        for w in workers:
            w.join(timeout=120)
        costs = []
        for w in workers:
            cost, ledger_net, finals = settled_cost(ctl, w.rid)
            costs.append(cost)
            report.check(
                f"burst {w.rid[-6:]} settled once and billed",
                w.error == "" and ledger_net == -cost and finals == 1 and cost > 0,
                f"error={w.error or '-'} cost={cost} ledger={ledger_net} finals={finals} chunks={w.chunks}",
            )
        delta = before - ctl.balance()
        report.check(
            "wallet debit equals sum of burst costs",
            delta == sum(costs),
            f"delta={delta} sum={sum(costs)} per_request={costs}",
        )

        # Slot must be free again after completion.
        rid, status, _ = post_json(
            burst_key,
            {"model": "deepseek-chat", "max_tokens": 8, "messages": [{"role": "user", "content": "Say ok."}]},
        )
        ok_release = status == 200
        if ok_release:
            cost, ledger_net, finals = settled_cost(ctl, rid)
            ok_release = ledger_net == -cost and finals == 1
        report.check("inflight slot released after streams finish", ok_release, f"http={status} rid={rid}")

        # --- RPM window -------------------------------------------------
        rpm_id, rpm_key = ctl.create_key(f"limits-rpm-{stamp}", 3)
        keys.append(rpm_id)
        statuses = []
        rids = []
        for i in range(4):
            rid, status, body = post_json(
                rpm_key,
                {"model": "deepseek-chat", "max_tokens": 8, "messages": [{"role": "user", "content": f"ping {i}"}]},
            )
            statuses.append((status, (body.get("error") or {}).get("code", "")))
            rids.append(rid)
        for i, rid in enumerate(rids[:3]):
            if rid:
                settled_cost(ctl, rid)
        report.check(
            "rpm=3 admits 3 then refuses the 4th",
            [s for s, _ in statuses[:3]] == [200, 200, 200]
            and statuses[3][0] == 429
            and statuses[3][1] == "rate_limit_exceeded",
            "statuses=" + ",".join(f"{s}/{c or '-'}" for s, c in statuses),
        )
    finally:
        for key_id in keys:
            try:
                ctl.revoke_key(key_id)
            except Exception:
                pass

    report.dump(args.report)
    print(f"\n{len(report.rows) - report.failures}/{len(report.rows)} checks passed; report -> {args.report}")
    return 1 if report.failures else 0


if __name__ == "__main__":
    sys.exit(main())
