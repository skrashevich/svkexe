#!/usr/bin/env python3
"""Exercise the standalone binary and its HTTP API, without the svkexe gateway."""

import argparse
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


class Provider(BaseHTTPRequestHandler):
    tool_seen = threading.Event()

    def log_message(self, *_):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if (self.path != "/v1/chat/completions"
                or self.headers.get("Authorization") != "Bearer smoke-key"
                or request.get("model") != "test/model"):
            self.send_error(400, "Wrong endpoint, credentials or model")
            return
        tool_reply = any(
            message.get("role") == "tool" and "standalone-tool-ok" in str(message.get("content"))
            for message in request.get("messages", [])
        )
        if tool_reply:
            self.tool_seen.set()
        message = {"role": "assistant", "content": "standalone-complete"}
        finish = "stop"
        if request.get("tools") and not tool_reply:
            message["content"] = ""
            message["tool_calls"] = [{
                "id": "standalone-call", "type": "function",
                "function": {"name": "bash", "arguments": json.dumps({
                    "command": "printf standalone-tool-ok > agent-proof.txt; cat agent-proof.txt"
                })},
            }]
            finish = "tool_calls"
        response = {
            "id": "standalone-completion", "object": "chat.completion", "model": "test/model",
            "choices": [{"index": 0, "message": message, "finish_reason": finish}],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
        }
        content_type = "application/json"
        if request.get("stream"):
            for call in message.get("tool_calls", []):
                call["index"] = 0
            response["object"] = "chat.completion.chunk"
            response["choices"] = [{"index": 0, "delta": message, "finish_reason": None}]
            first = json.dumps(response)
            response["choices"] = [{"index": 0, "delta": {}, "finish_reason": finish}]
            body = f"data: {first}\n\ndata: {json.dumps(response)}\n\ndata: [DONE]\n\n"
            content_type = "text/event-stream"
        else:
            body = json.dumps(response)
        data = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def request(base, path, payload=None, headers=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = Request(base + path, data=data, headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urlopen(req, timeout=15) as response:
            return response.status, response.read()
    except HTTPError as error:
        return error.code, error.read()


def api(base, path, payload=None, headers=None):
    status, data = request(base, path, payload, headers)
    assert status in (200, 201), (path, status, data)
    return json.loads(data)


@contextlib.contextmanager
def agent(binary, work, protected=False):
    port_file = work / "port"
    port_file.unlink(missing_ok=True)
    config_file = work / "picoclaw.json"
    config_file.write_text("{}")
    command = [str(binary), "-db", str(work / "picoclaw.db"), "-config", str(config_file),
               "-disable-llm-integration", "-disable-gateway", "serve", "-socket", "none",
               "-port", "0", "-port-file", str(port_file)]
    if protected:
        # Also exercise the explicit override, independently of the default.
        command.extend(["-host", "127.0.0.1", "-require-header", "X-Agent-User"])
    with (work / "agent.log").open("w+") as log:
        process = subprocess.Popen(command, cwd=work, stdout=log, stderr=log,
                                   env={"PATH": os.environ["PATH"], "HOME": str(work)})
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline and process.poll() is None:
                if port_file.exists() and port_file.read_text().strip():
                    base = "http://127.0.0.1:" + port_file.read_text().strip()
                    try:
                        if request(base, "/version")[0] == 200:
                            break
                    except (OSError, URLError):
                        pass
                time.sleep(0.05)
            else:
                raise AssertionError("Agent did not become ready")
            yield base
        except BaseException:
            log.flush()
            log.seek(0)
            print(log.read())
            raise
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def smoke(binary, app_name):
    with tempfile.TemporaryDirectory(prefix="picoclaw-smoke-") as directory:
        work = Path(directory).resolve()
        provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
        thread = threading.Thread(target=provider.serve_forever, daemon=True)
        thread.start()
        try:
            with agent(binary, work) as base:
                info = api(base, "/version")
                assert "picoclaw-engine" in info["capabilities"], info
                assert info["version"].startswith("picoclaw-"), info
                assert "svkexe" not in info["version"], info
                status, page = request(base, "/")
                assert status == 200 and b"<html" in page, (status, page[:200])
                manifest = api(base, "/manifest.json")
                assert manifest["name"] == app_name, manifest
                version = api(base, "/version-check")
                assert not version["has_update"] and not version.get("download_url"), version
                status, error = request(base, "/upgrade", {})
                assert status >= 400 and b"managed externally" in error, (status, error)
                model = api(base, "/api/custom-models", {
                    "display_name": "Standalone test", "provider_type": "openai",
                    "endpoint": f"http://127.0.0.1:{provider.server_port}/v1",
                    "api_key": "smoke-key", "model_name": "test/model", "max_tokens": 8192,
                })
                conversation = api(base, "/api/conversations/new", {
                    "message": "Write and read the proof file using bash, then report success.",
                    "model": model["model_id"], "cwd": str(work),
                })
                conversation_id = conversation["conversation_id"]
                complete = False
                with urlopen(base + f"/api/conversation/{conversation_id}/stream", timeout=30) as stream:
                    assert "text/event-stream" in stream.headers["Content-Type"]
                    for line in stream:
                        if Provider.tool_seen.is_set() and b"standalone-complete" in line:
                            complete = True
                            break
                assert complete and Provider.tool_seen.is_set(), "LLM/tool/stream round trip incomplete"
                assert (work / "agent-proof.txt").read_text() == "standalone-tool-ok"
            with agent(binary, work, protected=True) as base:
                assert request(base, "/api/models")[0] in (401, 403), "Missing identity accepted"
                headers = {"X-Agent-User": "smoke-user"}
                api(base, "/api/models", headers=headers)
                history = api(base, f"/api/conversation/{conversation_id}", headers=headers)
                text = json.dumps(history)
                assert "standalone-complete" in text and "standalone-tool-ok" in text, history
        finally:
            provider.shutdown()
            provider.server_close()
            thread.join(timeout=5)
    print("PASS: standalone UI/API, model auth, real bash, SSE, restart/history, identity header, manual updates")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--app-name", default=os.environ.get("AGENT_APP_NAME", "PicoClaw"))
    args = parser.parse_args()
    smoke(args.binary.resolve(), args.app_name)
