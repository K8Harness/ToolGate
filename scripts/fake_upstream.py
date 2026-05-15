#!/usr/bin/env python3

import json
import time
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/mcp":
            self.send_error(404)
            return

        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        request = json.loads(body or b"{}")

        if request.get("method") == "initialize":
            response = {
                "jsonrpc": "2.0",
                "id": request.get("id"),
                "result": {"server": "fake-upstream", "ok": True},
            }
        else:
            params = request.get("params") or {}
            arguments = params.get("arguments") or {}
            hold_ms = int(arguments.get("hold_ms", 0) or 0)
            if hold_ms > 0:
                time.sleep(hold_ms / 1000.0)
            response = {
                "jsonrpc": "2.0",
                "id": request.get("id"),
                "result": {
                    "ok": True,
                    "tool": params.get("name"),
                    "arguments": arguments,
                },
            }

        payload = json.dumps(response).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, format, *args):
        return


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8081), Handler).serve_forever()
