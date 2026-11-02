#!/usr/bin/env python3
"""Price-versioning and audit-trail checks for teemoHub.

Drives the real control plane: every model must carry an append-only price
version, a price edit must append a new version plus an audit event, a billed
request must snapshot the version that priced it, and both tables must reject
UPDATE/DELETE at the SQLite trigger level.

State it creates: two short-lived price versions that restore the original
prices, one short-lived key (revoked at the end), and one tiny request.
Usage: python3 scripts/audit.py [--report runtime/audit-report.json]
"""

from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request

CONTROL = "http://127.0.0.1:18080"
GATEWAY = "http://127.0.0.1:18081/v1/chat/completions"
ADMIN = ("admin", "TeemoAdmin!2026")
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DB = os.path.join(ROOT, "runtime", "teemohub.db")


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


def triggers_reject(sql: str) -> tuple[bool, str]:
    """True when SQLite refuses the statement, which proves the guard fired."""
    try:
        proc = subprocess.run(
            ["sqlite3", DB, sql], capture_output=True, text=True, timeout=10
        )
    except (OSError, subprocess.SubprocessError) as e:
        return False, str(e)
    out = (proc.stderr or proc.stdout).strip().splitlines()
    return proc.returncode != 0, (out[0] if out else f"exit {proc.returncode}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", default="runtime/audit-report.json")
    args = ap.parse_args()

    ctl = Control()
    ctl.login()
    report = Report()

    status, payload = ctl.call("/api/admin/models")
    models = payload.get("items") or []
    missing = [m["id"] for m in models if int(m.get("price_version_id") or 0) == 0]
    report.check(
        "every model carries a price version",
        status == 200 and bool(models) and not missing,
        f"models={len(models)} missing_versions={missing}",
    )

    model = next(m for m in models if m["id"] == "deepseek-chat")
    original_input = int(model["input_price_nano"])
    bumped_input = original_input + 1  # +0.01 CNY/M
    key_id = 0
    request_rid = ""
    bump_vid = restore_vid = 0
    try:
        status, payload = ctl.call(
            f"/api/admin/models/deepseek-chat",
            "PUT",
            {"input_price_cny_per_m": bumped_input / 100, "note": "audit-check bump"},
        )
        bump_vid = int(payload.get("price_version_id") or 0)
        report.check(
            "price edit appends a new version",
            status == 200 and bump_vid > int(model["price_version_id"]) and int(payload["model"]["input_price_nano"]) == bumped_input,
            f"version {model['price_version_id']} -> {bump_vid} input={payload.get('model', {}).get('input_price_nano')}",
        )

        status, payload = ctl.call("/api/admin/models/deepseek-chat/versions?limit=10")
        versions = payload.get("items") or []
        bump_row = next((v for v in versions if int(v["id"]) == bump_vid), None)
        restore_later = next((v for v in versions if int(v["id"]) == int(model["price_version_id"])), None)
        report.check(
            "version history keeps the previous rate card intact",
            status == 200
            and bump_row is not None
            and int(bump_row["price"]["input_price_nano"]) == bumped_input
            and restore_later is not None
            and int(restore_later["price"]["input_price_nano"]) == original_input,
            f"versions={[v['id'] for v in versions]}",
        )

        status, payload = ctl.call(
            "/api/admin/audit?resource=model&action=model.prices.update&limit=5"
        )
        events = payload.get("items") or []
        match = next((e for e in events if e.get("resource_id") == "deepseek-chat"), None)
        after = json.loads(match["after_json"]) if match and match.get("after_json") else {}
        before = json.loads(match["before_json"]) if match and match.get("before_json") else {}
        report.check(
            "price edit is audited with before/after",
            status == 200
            and match is not None
            and match.get("action") == "model.prices.update"
            and after.get("price_version_id") == bump_vid
            and before.get("price_version_id") == int(model["price_version_id"]),
            f"event={match.get('id') if match else None} before={before.get('price_version_id')} after={after.get('price_version_id')}",
        )

        status, payload = ctl.call(
            "/api/keys",
            "POST",
            {"name": f"audit-{int(time.time())}", "rpm": 60, "tpm": 5000, "concurrent": 2, "max_output": 16},
        )
        if status != 200:
            raise RuntimeError(f"create key failed: {status} {payload}")
        key_id = int(payload["key"]["id"])
        key = payload["secret"]

        status, payload = ctl.call("/api/admin/audit?resource=api_key&action=key.create&limit=5")
        events = payload.get("items") or []
        created = next((e for e in events if e.get("resource_id") == str(key_id)), None)
        after = json.loads(created["after_json"]) if created and created.get("after_json") else {}
        report.check(
            "key creation is audited with its quotas",
            created is not None
            and after.get("tpm") == 5000
            and after.get("concurrent") == 2
            and after.get("max_output") == 16
            and "key_hash" not in after
            and "secret" not in after,
            f"after keys={sorted(after)}" if created else "missing key.create event",
        )

        body = {
            "model": "deepseek-chat",
            "max_tokens": 8,
            "messages": [{"role": "user", "content": "Reply with exactly: audit-ok"}],
        }
        req = urllib.request.Request(
            GATEWAY,
            data=json.dumps(body).encode(),
            headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                request_rid = resp.getheader("X-TeemoHub-Request-Id") or ""
                resp.read()
        except urllib.error.HTTPError as e:
            request_rid = e.headers.get("X-TeemoHub-Request-Id") or ""
            raise RuntimeError(f"gateway request failed: {e.code} {e.read()[:200]}")

        detail = ctl.wait_settled(request_rid)
        r = detail["request"]
        v = detail.get("price_version") or {}
        report.check(
            "billed request snapshots the version that priced it",
            r["status"] == "completed"
            and int(r["price_version_id"]) == bump_vid
            and int((v.get("price") or {}).get("input_price_nano") or -1) == bumped_input,
            f"rid={request_rid} version={r['price_version_id']} input={(v.get('price') or {}).get('input_price_nano')}",
        )

        status, payload = ctl.call(
            f"/api/admin/models/deepseek-chat",
            "PUT",
            {"input_price_cny_per_m": original_input / 100, "note": "audit-check restore"},
        )
        restore_vid = int(payload.get("price_version_id") or 0)
        report.check(
            "prices can be restored as another append",
            status == 200 and int(payload["model"]["input_price_nano"]) == original_input and restore_vid > bump_vid,
            f"version {bump_vid} -> {restore_vid} input={payload.get('model', {}).get('input_price_nano')}",
        )

        detail = ctl.request(request_rid)
        report.check(
            "historical request still points at its own version",
            int(detail["request"]["price_version_id"]) == bump_vid,
            f"rid={request_rid} still on version {detail['request']['price_version_id']}",
        )

        ctl.call(f"/api/keys/{key_id}", "DELETE")
        status, payload = ctl.call("/api/admin/audit?resource=api_key&action=key.revoke&limit=5")
        revoked = next(
            (e for e in (payload.get("items") or []) if e.get("resource_id") == str(key_id)),
            None,
        )
        report.check(
            "key revocation is audited",
            revoked is not None,
            f"event={revoked.get('id') if revoked else None}",
        )
        key_id = 0

        status, first = ctl.call("/api/admin/audit?limit=1")
        head = (first.get("items") or [{}])[0]
        status2, older = ctl.call(f"/api/admin/audit?limit=1&before_id={head.get('id', 0)}")
        second = (older.get("items") or [{}])[0]
        report.check(
            "audit pagination walks backwards by id",
            status == 200
            and status2 == 200
            and len(first.get("items") or []) == 1
            and len(older.get("items") or []) == 1
            and int(second.get("id") or 0) < int(head.get("id") or 0),
            f"{head.get('id')} -> {second.get('id')}",
        )

        ok, why = triggers_reject(
            "UPDATE model_price_versions SET input_price=input_price+1 WHERE id=(SELECT MAX(id) FROM model_price_versions);"
        )
        report.check("price versions reject UPDATE", ok, why)
        ok, why = triggers_reject("DELETE FROM model_price_versions WHERE 1;")
        report.check("price versions reject DELETE", ok, why)
        ok, why = triggers_reject("UPDATE audit_events SET reason='tamper' WHERE 1;")
        report.check("audit events reject UPDATE", ok, why)
        ok, why = triggers_reject("DELETE FROM audit_events WHERE 1;")
        report.check("audit events reject DELETE", ok, why)

        con = sqlite3.connect(DB)
        try:
            latest = con.execute(
                "SELECT input_price FROM model_price_versions WHERE id=?", (restore_vid,)
            ).fetchone()
        finally:
            con.close()
        report.check(
            "rejected writes left the rate card untouched",
            latest is not None and int(latest[0]) == original_input,
            f"version {restore_vid} input={latest[0] if latest else None}",
        )
    finally:
        if key_id:
            try:
                ctl.call(f"/api/keys/{key_id}", "DELETE")
            except Exception:
                pass
        if restore_vid == 0:
            try:
                ctl.call(
                    f"/api/admin/models/deepseek-chat",
                    "PUT",
                    {"input_price_cny_per_m": original_input / 100, "note": "audit-check cleanup restore"},
                )
            except Exception:
                pass

    report.dump(args.report)
    print(f"\n{len(report.rows) - report.failures}/{len(report.rows)} checks passed; report -> {args.report}")
    return 1 if report.failures else 0


if __name__ == "__main__":
    sys.exit(main())
