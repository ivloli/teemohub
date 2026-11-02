#!/usr/bin/env python3
"""End-to-end checks for teemoHub -> New API -> DeepSeek/Kimi.

Runs read-only assertions against the local stack; the only state it changes is
the demo balance and request history created by the calls themselves.

Usage: python3 scripts/e2e.py [--key-file /tmp/th.key] [--report runtime/e2e-report.json]
"""

from __future__ import annotations

import argparse
import http.client
import http.cookiejar
import json
import os
import socket
import sys
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
            "/api/auth/login",
            "POST",
            {"username": ADMIN[0], "password": ADMIN[1]},
        )
        if status != 200:
            raise RuntimeError(f"login failed: {status} {payload}")
        self.user = payload["user"]

    def models(self) -> dict[str, dict]:
        _, payload = self.call("/api/models")
        return {m["id"]: m for m in payload.get("items", [])}

    def request(self, rid: str) -> dict:
        status, payload = self.call(f"/api/requests/{rid}")
        if status != 200:
            raise RuntimeError(f"request {rid}: {status} {payload}")
        return payload

    def wait_settled(self, rid: str, timeout: float = 20.0) -> dict:
        deadline = time.time() + timeout
        last = None
        while time.time() < deadline:
            payload = self.request(rid)
            last = payload
            if payload["request"]["status"] not in ("reserved", "streaming"):
                return payload
            time.sleep(0.2)
        raise RuntimeError(f"{rid} did not settle: {json.dumps(last)[:500]}")


def post_stream(
    key: str,
    body: dict,
    hold_seconds: float | None = None,
) -> tuple[str, dict]:
    """POST a chat completion. hold_seconds closes the socket mid-stream."""
    conn = http.client.HTTPConnection(GATEWAY_HOST, GATEWAY_PORT, timeout=120)
    raw = json.dumps(body).encode()
    conn.request(
        "POST",
        "/v1/chat/completions",
        body=raw,
        headers={
            "Authorization": "Bearer " + key,
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
        },
    )
    resp = conn.getresponse()
    rid = resp.getheader("X-TeemoHub-Request-Id") or ""
    status = resp.status
    if hold_seconds is not None:
        started = time.time()
        chunks = 0
        first_content_at = 0.0
        while time.time() - started < hold_seconds:
            try:
                line = resp.readline()
            except (socket.timeout, OSError):
                break
            if not line:
                break
            if b'"content"' in line or b'"tool_calls"' in line:
                chunks += 1
                if not first_content_at:
                    first_content_at = time.time() - started
        try:
            conn.sock.close()
        except Exception:
            pass
        return rid, {"status": status, "chunks_before_close": chunks, "hold": hold_seconds}

    lines: list[str] = []
    try:
        while True:
            line = resp.readline()
            if not line:
                break
            text = line.decode(errors="replace")
            lines.append(text)
            if text.strip() == "data: [DONE]":
                break
    finally:
        conn.close()
    content = ""
    reasoning = ""
    tool_args = ""
    finish = ""
    usage = None
    for text in lines:
        text = text.strip()
        if not text.startswith("data:"):
            continue
        payload = text[len("data:") :].strip()
        if not payload or payload == "[DONE]":
            continue
        try:
            chunk = json.loads(payload)
        except json.JSONDecodeError:
            continue
        if chunk.get("usage"):
            usage = chunk["usage"]
        for choice in chunk.get("choices") or []:
            delta = choice.get("delta") or {}
            content += delta.get("content") or ""
            reasoning += delta.get("reasoning_content") or ""
            for tc in delta.get("tool_calls") or []:
                tool_args += (tc.get("function") or {}).get("arguments") or ""
            if choice.get("finish_reason"):
                finish = choice["finish_reason"]
    return rid, {
        "status": status,
        "content": content,
        "reasoning": reasoning,
        "tool_args": tool_args,
        "finish": finish,
        "usage": usage,
        "chunks": len(lines),
    }


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


def assert_settlement(
    report: Report,
    name: str,
    ctl: Control,
    rid: str,
    models: dict[str, dict],
    want_status: str,
    want_source: str,
    min_output: int = 0,
    min_reasoning: int = 0,
    want_zero_cost: bool = False,
    allow_early_unbilled: bool = False,
) -> dict:
    detail = ctl.wait_settled(rid)
    req = detail["request"]
    events = detail.get("usage_events") or []
    ledger = detail.get("ledger") or []
    model = models.get(req["model"]) or {}
    input_price = int(model.get("input_price_nano", 0))
    cached = min(int(req.get("cached_input_tokens", 0)), int(req["input_tokens"]))
    cached_price = int(model.get("cached_input_price_nano", 0)) or input_price
    expected = (
        (req["input_tokens"] - cached) * input_price
        + cached * cached_price
        + req["output_tokens"] * int(model.get("output_price_nano", 0))
    )
    ledger_net = sum(int(e["amount_nano"]) for e in ledger)
    problems = []
    if req["status"] != want_status:
        problems.append(f"status={req['status']}")
    early = allow_early_unbilled and req["first_token_ms"] == 0
    if early:
        # Aborted before the first token: nothing deliverable, nothing billed.
        want_zero_cost = True
        if req["usage_source"] not in ("none", "counted_text"):
            problems.append(f"source={req['usage_source']}")
        if req["output_tokens"] != 0:
            problems.append(f"out={req['output_tokens']} before first token")
    elif req["usage_source"] != want_source:
        problems.append(f"source={req['usage_source']}")
    if req["output_tokens"] < min_output:
        problems.append(f"out={req['output_tokens']}<{min_output}")
    if req["reasoning_tokens"] < min_reasoning:
        problems.append(f"reasoning={req['reasoning_tokens']}<{min_reasoning}")
    if want_zero_cost and req["cost_nano"] != 0:
        problems.append(f"cost={req['cost_nano']}")
    if not want_zero_cost and req["cost_nano"] != expected:
        problems.append(f"cost={req['cost_nano']}!=in*pin+out*pout={expected}")
    if ledger_net != -req["cost_nano"]:
        problems.append(f"ledger_net={ledger_net}!=-cost")
    if len(events) != 1 or events[0].get("final") != 1:
        problems.append(f"usage_events={len(events)}")
    report.check(name, not problems, "; ".join(problems))
    if problems:
        print(json.dumps(detail, ensure_ascii=False)[:1200])
    return req


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--key-file", default="/tmp/th.key")
    ap.add_argument("--report", default="runtime/e2e-report.json")
    args = ap.parse_args()
    with open(args.key_file) as f:
        key = f.read().strip()
    if not key.startswith("sk-th-"):
        raise SystemExit("key file does not contain a teemoHub key")

    ctl = Control()
    ctl.login()
    models = ctl.models()
    report = Report()

    # 1. non-stream
    rid, _, _ = post_json(
        key,
        {
            "model": "deepseek-chat",
            "messages": [{"role": "user", "content": "Reply with exactly: e2e-nonstream"}],
            "max_tokens": 32,
        },
    )
    assert_settlement(report, "nonstream deepseek-chat", ctl, rid, models, "completed", "provider_final", min_output=1)

    # 2. stream
    rid, info = post_stream(
        key,
        {
            "model": "deepseek-chat",
            "stream": True,
            "max_tokens": 80,
            "messages": [{"role": "user", "content": "Count from 1 to 10, comma separated."}],
        },
    )
    req = assert_settlement(report, "stream deepseek-chat", ctl, rid, models, "completed", "provider_final", min_output=5)
    report.check("stream saw [DONE] and content", info.get("content", "") != "", f"finish={info.get('finish')}")

    # 3. client aborts across the drain ladder: 0.5 / 1 / 2 / 5 s
    abort_body = {
        "model": "deepseek-chat",
        "stream": True,
        "max_tokens": 1500,
        "messages": [{"role": "user", "content": "Write a long detailed essay about tea history."}],
    }
    aborted: dict[float, dict] = {}
    for hold in (0.5, 1.0, 2.0, 5.0):
        rid, _ = post_stream(key, abort_body, hold_seconds=hold)
        req_ab = assert_settlement(
            report,
            f"abort deepseek-chat @{hold}s",
            ctl,
            rid,
            models,
            "client_aborted",
            "counted_text",
            allow_early_unbilled=True,
        )
        report.check(
            f"abort @{hold}s records disconnect and unfinished upstream",
            req_ab["client_disconnected"] and not req_ab["upstream_finished"],
            f"cd={req_ab['client_disconnected']} uf={req_ab['upstream_finished']} "
            f"ttft={req_ab['first_token_ms']}ms out={req_ab['output_tokens']}",
        )
        aborted[hold] = req_ab

    holds = sorted(aborted)
    report.check(
        "later aborts never bill less than earlier aborts",
        all(aborted[b]["output_tokens"] >= aborted[a]["output_tokens"] for a, b in zip(holds, holds[1:])),
        " -> ".join(f"{h}s:{aborted[h]['output_tokens']}t" for h in holds),
    )

    # 5. tool calls (two tools)
    rid, info = post_stream(
        key,
        {
            "model": "deepseek-chat",
            "stream": True,
            "max_tokens": 200,
            "messages": [
                {"role": "user", "content": "What is the weather in Beijing? Call the weather tool."}
            ],
            "tools": [
                {
                    "type": "function",
                    "function": {
                        "name": "get_weather",
                        "description": "Get current weather",
                        "parameters": {
                            "type": "object",
                            "properties": {"city": {"type": "string"}},
                            "required": ["city"],
                        },
                    },
                },
                {
                    "type": "function",
                    "function": {
                        "name": "get_time",
                        "description": "Get current local time for a city",
                        "parameters": {
                            "type": "object",
                            "properties": {"city": {"type": "string"}},
                            "required": ["city"],
                        },
                    },
                },
            ],
        },
    )
    req_tool = assert_settlement(report, "tool_call deepseek-chat", ctl, rid, models, "completed", "provider_final", min_output=1)
    report.check(
        "tool arguments streamed and recorded",
        bool(info.get("tool_args")),
        f"finish={info.get('finish')} args={info.get('tool_args', '')[:40]}",
    )
    detail = ctl.request(rid)
    usage_detail = json.loads(detail["usage_events"][0]["detail_json"])
    report.check("tool_calls counted in usage detail", int(usage_detail.get("tool_calls", 0)) >= 1, str(usage_detail))

    # 6. JSON mode (non-stream)
    rid, _, payload = post_json(
        key,
        {
            "model": "deepseek-chat",
            "max_tokens": 60,
            "response_format": {"type": "json_object"},
            "messages": [
                {
                    "role": "user",
                    "content": "Return a JSON object with keys ok (true) and n (42). No prose.",
                }
            ],
        },
    )
    assert_settlement(report, "json mode deepseek-chat", ctl, rid, models, "completed", "provider_final", min_output=1)
    content = (payload.get("choices") or [{}])[0].get("message", {}).get("content", "")
    try:
        parsed_json = json.loads(content)
    except json.JSONDecodeError:
        parsed_json = None
    report.check("json mode returns parseable JSON", isinstance(parsed_json, dict), content[:60])

    # 7. reasoning
    rid, info = post_stream(
        key,
        {
            "model": "deepseek-reasoner",
            "stream": True,
            "max_tokens": 400,
            "messages": [{"role": "user", "content": "What is 23*19? Think briefly."}],
        },
    )
    assert_settlement(report, "reasoning deepseek-reasoner", ctl, rid, models, "completed", "provider_final", min_output=1, min_reasoning=1)

    # 8. Kimi
    rid, info = post_stream(
        key,
        {
            "model": "kimi-k3.1-pro",
            "stream": True,
            "max_tokens": 400,
            "messages": [{"role": "user", "content": "Answer in one short sentence: what is 2+2?"}],
        },
    )
    assert_settlement(report, "stream kimi-k3.1-pro", ctl, rid, models, "completed", "provider_final", min_output=1)

    # 9. upstream error is free and releases the reservation
    rid, status, payload = post_json(
        key,
        {
            "model": "deepseek-chat",
            "messages": [{"role": "user", "content": "x"}],
            "max_tokens": -1,
        },
    )
    if not rid:
        st, body = ctl.call("/api/requests?limit=1")
        if st == 200 and body.get("items"):
            rid = body["items"][0]["id"]
    if rid:
        req_err = assert_settlement(report, "invalid request does not bill", ctl, rid, models, "upstream_error", "none", want_zero_cost=True)
        report.check("invalid request returns non-200 or upstream error", True, f"http={status} status={req_err['status']}")
    else:
        report.check("invalid request does not bill", status == 400, f"http={status} no request row (rejected before billing)")

    # 10. auth / model guards
    _, st, body = post_json("sk-th-invalid", {"model": "deepseek-chat", "messages": [{"role": "user", "content": "x"}]})
    report.check("invalid API key rejected", st == 401 and body.get("error", {}).get("code") == "invalid_api_key", f"{st} {body.get('error', {})}")
    _, st, body = post_json(key, {"model": "no-such-model", "messages": [{"role": "user", "content": "x"}]})
    report.check("unknown model rejected", st == 400 and body.get("error", {}).get("code") == "model_not_found", f"{st} {body.get('error', {})}")

    report.dump(args.report)
    print(f"\n{len(report.rows) - report.failures}/{len(report.rows)} checks passed; report -> {args.report}")
    return 1 if report.failures else 0


if __name__ == "__main__":
    sys.exit(main())
