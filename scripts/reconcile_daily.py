#!/usr/bin/env python3
"""Daily reconciliation: teemoHub ledger/requests vs New API consumption logs.

Reads teemoHub's SQLite database read-only and New API's admin log API, then
compares per local-day/per-model request counts and token totals. Aborted
requests are billed from local estimates, so their token deltas are reported as
an expected band instead of a mismatch.

Optionally accepts a provider export (JSON) with actual daily cost per family:

    {"deepseek": {"2026-10-29": {"cost_cny": 0.029966, "requests": 5}},
     "kimi":     {"2026-10-29": {"cost_cny": 0.039070, "requests": 1}}}

Nothing is written anywhere except the local report file; New API is only read.

Usage:
  python3 scripts/reconcile_daily.py [--days 7] [--token-name teemohub-gateway]
                                     [--provider-file path.json] [--strict]
"""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import http.cookiejar
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENV_PATH = os.path.join(ROOT, ".env")
DB_PATH = os.path.join(ROOT, "runtime", "teemohub.db")
NEWAPI_DEFAULT = "http://127.0.0.1:3000"
BILLABLE = ("completed", "drained", "client_aborted")


def load_env(path: str) -> dict[str, str]:
    env: dict[str, str] = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"').strip("'")
    return env


class NewAPI:
    def __init__(self, base: str, username: str, password: str) -> None:
        self.base = base.rstrip("/")
        self.username = username
        self.password = password
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
        )
        self.auth: dict[str, str] = {}

    def _call(self, path: str, method: str = "GET", body=None) -> tuple[int, dict]:
        data = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json", **self.auth}
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with self.opener.open(req, timeout=20) as resp:
                return resp.status, json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as e:
            try:
                return e.code, json.loads(e.read() or b"{}")
            except json.JSONDecodeError:
                return e.code, {}

    def login(self) -> None:
        status, payload = self._call(
            "/api/user/login", "POST", {"username": self.username, "password": self.password}
        )
        if status != 200 or not payload.get("success"):
            raise RuntimeError(f"new api login failed: http {status} {payload.get('message')}")
        data = payload["data"]
        self.auth = {
            "Authorization": "Bearer " + data["access_token"],
            "New-Api-User": str(data["user"]["id"]),
        }

    def consumption_logs(self, start: int, end: int) -> list[dict]:
        rows: list[dict] = []
        page = 0
        while True:
            status, payload = self._call(
                f"/api/log/?p={page}&page_size=100&type=0&start_timestamp={start}&end_timestamp={end}"
            )
            if status != 200 or not payload.get("success"):
                raise RuntimeError(f"new api log failed: http {status} {payload.get('message')}")
            data = payload.get("data") or {}
            items = data.get("items") or []
            rows.extend(items)
            total = int(data.get("total") or 0)
            if not items or len(rows) >= total:
                return rows
            page += 1
            if page > 500:
                raise RuntimeError("new api log pagination runaway")


def day_of(ts_ms_or_s: float, is_ms: bool) -> str:
    ts = ts_ms_or_s / 1000.0 if is_ms else float(ts_ms_or_s)
    return dt.datetime.fromtimestamp(ts).strftime("%Y-%m-%d")


def load_local(path: str, since_ms: int) -> tuple[dict[tuple[str, str], dict], list[dict]]:
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    agg: dict[tuple[str, str], dict] = {}
    q = """
      SELECT date(created_at/1000,'unixepoch','localtime') AS day, model,
             COUNT(1) AS requests,
             SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END) AS completed,
             SUM(CASE WHEN status='drained' THEN 1 ELSE 0 END) AS drained,
             SUM(CASE WHEN status='client_aborted' THEN 1 ELSE 0 END) AS aborted,
             SUM(CASE WHEN status='upstream_error' THEN 1 ELSE 0 END) AS errored,
             SUM(input_tokens) AS input_tokens,
             SUM(output_tokens) AS output_tokens,
             SUM(cached_input_tokens) AS cached_input,
             SUM(cost_nano) AS cost_nano,
             SUM(provider_cost_nano) AS provider_cost_nano
      FROM requests WHERE created_at>=? GROUP BY day, model
    """
    for r in db.execute(q, (since_ms,)):
        if r["day"] is None:
            continue
        agg[(r["day"], r["model"])] = {
            "requests": r["requests"],
            "billable": r["completed"] + r["drained"] + r["aborted"],
            "completed": r["completed"],
            "drained": r["drained"],
            "aborted": r["aborted"],
            "errored": r["errored"],
            "input_tokens": r["input_tokens"] or 0,
            "output_tokens": r["output_tokens"] or 0,
            "cached_input": r["cached_input"] or 0,
            "cost_nano": r["cost_nano"] or 0,
            "provider_cost_nano": r["provider_cost_nano"] or 0,
        }
    rows = [
        dict(r)
        for r in db.execute(
            "SELECT id, model, status, input_tokens, output_tokens, created_at "
            "FROM requests WHERE created_at>=? AND status IN ('completed','drained','client_aborted')",
            (since_ms,),
        )
    ]
    db.close()
    return agg, rows


def load_newapi(rows: list[dict], token_name: str) -> tuple[dict[tuple[str, str], dict], list[dict]]:
    agg: dict[tuple[str, str], dict] = {}
    detail: list[dict] = []
    for r in rows:
        if token_name and r.get("token_name") != token_name:
            continue
        if int(r.get("type", 0)) != 2:  # 2 = consumption log
            continue
        day = day_of(r["created_at"], is_ms=False)
        model = r.get("model_name") or ""
        key = (day, model)
        a = agg.setdefault(
            key,
            {"requests": 0, "input_tokens": 0, "output_tokens": 0, "cached_input": 0, "quota": 0,
             "aborted_hint": 0},
        )
        a["requests"] += 1
        a["input_tokens"] += int(r.get("prompt_tokens") or 0)
        a["output_tokens"] += int(r.get("completion_tokens") or 0)
        a["quota"] += int(r.get("quota") or 0)
        end_reason = ""
        other = r.get("other")
        if isinstance(other, str) and other:
            try:
                meta = json.loads(other)
                a["cached_input"] += int(meta.get("cache_tokens") or 0)
                end_reason = str(((meta.get("stream_status") or {}).get("end_reason")) or "")
                if end_reason and end_reason != "done":
                    a["aborted_hint"] += 1
            except (json.JSONDecodeError, AttributeError):
                pass
        detail.append(
            {
                "day": day,
                "model": model,
                "id": r.get("id"),
                "created_at": r["created_at"],
                "prompt_tokens": int(r.get("prompt_tokens") or 0),
                "completion_tokens": int(r.get("completion_tokens") or 0),
                "end_reason": end_reason or "done",
            }
        )
    return agg, detail


def classify_remote(remote_rows: list[dict], local_rows: list[dict]) -> dict[int, str]:
    """Map New API row index -> completed | aborted_local | unexplained."""
    by_model: dict[str, list[dict]] = collections.defaultdict(list)
    for idx, l in enumerate(local_rows):
        by_model[l["model"]].append({**l, "_idx": idx})
    used: set[int] = set()
    verdict: dict[int, str] = {}

    def take(pred) -> bool:
        for l in pred():
            if l["_idx"] in used:
                continue
            used.add(l["_idx"])
            return True
        return False

    for i, r in enumerate(remote_rows):
        model = r["model"]
        near_completed = lambda: (
            l
            for l in by_model.get(model, [])
            if l["status"] == "completed"
            and l["input_tokens"] == r["prompt_tokens"]
            and l["output_tokens"] == r["completion_tokens"]
            and abs(l["created_at"] / 1000 - r["created_at"]) <= 60
        )
        near_aborted = lambda: (
            l
            for l in by_model.get(model, [])
            if l["status"] in ("client_aborted", "drained")
            and abs(l["created_at"] / 1000 - r["created_at"]) <= 300
        )
        if r["end_reason"] == "done" and take(near_completed):
            verdict[i] = "completed"
        elif take(near_aborted):
            verdict[i] = "aborted_local"
        else:
            verdict[i] = "unexplained"
    return verdict


def pct(delta: int, base: int) -> str:
    if base == 0:
        return "n/a"
    return f"{delta / base * 100:+.1f}%"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--days", type=int, default=7, help="lookback window in local days")
    ap.add_argument("--token-name", default="teemohub-gateway", help="New API token name used by teemoHub")
    ap.add_argument("--provider-file", help="optional JSON with provider console daily actuals")
    ap.add_argument("--out", default=os.path.join(ROOT, "runtime", "reconcile-report.json"))
    ap.add_argument("--strict", action="store_true", help="exit 1 when request counts differ")
    args = ap.parse_args()

    env = load_env(ENV_PATH)
    base = env.get("NEWAPI_BASE_URL") or NEWAPI_DEFAULT
    start_day = dt.date.today() - dt.timedelta(days=args.days)
    start_ts = int(time.mktime(start_day.timetuple()))
    end_ts = int(time.mktime((dt.date.today() + dt.timedelta(days=1)).timetuple()))
    since_ms = start_ts * 1000

    api = NewAPI(base, env.get("NEWAPI_ROOT_USER", ""), env.get("NEWAPI_ROOT_PASSWORD", ""))
    api.login()
    logs = api.consumption_logs(start_ts, end_ts)

    local, local_rows = load_local(DB_PATH, since_ms)
    remote, remote_rows = load_newapi(logs, args.token_name)
    verdict = classify_remote(remote_rows, local_rows)
    unexplained_by_key: dict[tuple[str, str], list[dict]] = collections.defaultdict(list)
    for i, row in enumerate(remote_rows):
        if verdict[i] == "unexplained":
            unexplained_by_key[(row["day"], row["model"])].append(
                {"id": row["id"], "at": dt.datetime.fromtimestamp(row["created_at"]).strftime("%H:%M:%S"),
                 "in": row["prompt_tokens"], "out": row["completion_tokens"]}
            )

    provider_actuals: list[dict] = []
    if args.provider_file:
        with open(args.provider_file, encoding="utf-8") as fh:
            exports = json.load(fh)
        for family, by_day in exports.items():
            for day, facts in by_day.items():
                provider_actuals.append(
                    {"family": family, "day": day, "cost_cny": float(facts.get("cost_cny", 0))}
                )

    keys = sorted(set(local) | set(remote))
    rows_out = []
    mismatched = 0
    print(f"reconcile window: {start_day} .. {dt.date.today()}  token={args.token_name}")
    header = f"{'day':10} {'model':20} {'req(th)':>7} {'req(na)':>7} {'in(th)':>9} {'in(na)':>9} {'out(th)':>9} {'out(na)':>9} {'abort':>5} {'orph':>4} {'status':>8}"
    print(header)
    print("-" * len(header))
    for day, model in keys:
        l = local.get((day, model))
        r = remote.get((day, model))
        if l is None and r is None:
            continue
        orphans = unexplained_by_key.get((day, model), [])
        l_req = l["billable"] if l else 0
        r_req = r["requests"] if r else 0
        status = "ok"
        if l_req != r_req:
            status = "review"
            mismatched += 1
        l_in = l["input_tokens"] if l else 0
        r_in = r["input_tokens"] if r else 0
        l_out = l["output_tokens"] if l else 0
        r_out = r["output_tokens"] if r else 0
        aborted = l["aborted"] + l["drained"] if l else 0
        print(
            f"{day:10} {model:20} {l_req:7d} {r_req:7d} {l_in:9d} {r_in:9d} {l_out:9d} {r_out:9d} {aborted:5d} {len(orphans):4d} {status:>8}"
        )
        notes = []
        if aborted:
            notes.append("aborted/drained token deltas are local estimates (expected band)")
        if orphans:
            notes.append(
                "New API rows with no teemoHub counterpart: "
                + ", ".join(f"#{o['id']}@{o['at']} {o['in']}/{o['out']}" for o in orphans[:5])
            )
        rows_out.append(
            {
                "day": day,
                "model": model,
                "teemohub": l or {},
                "newapi": r or {},
                "request_delta": l_req - r_req,
                "input_delta": l_in - r_in,
                "input_delta_pct": pct(l_in - r_in, r_in),
                "output_delta": l_out - r_out,
                "output_delta_pct": pct(l_out - r_out, r_out),
                "unexplained_newapi": orphans,
                "status": status,
                "note": "; ".join(notes),
            }
        )

    report = {
        "generated_at": dt.datetime.now().isoformat(timespec="seconds"),
        "window": {"start": str(start_day), "end": str(dt.date.today()), "days": args.days},
        "token_name": args.token_name,
        "rows": rows_out,
        "request_mismatches": mismatched,
        "provider_actuals": provider_actuals,
    }
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(report, fh, ensure_ascii=False, indent=2)
    print(f"\nrows={len(rows_out)} request_mismatches={mismatched} report -> {args.out}")
    if args.strict and mismatched:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
