#!/usr/bin/env python3

import json
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

TOOLS = [
    {
        "name": "refund_small",
        "description": "Process a small refund",
        "inputSchema": {
            "type": "object",
            "properties": {
                "amount": {"type": "integer"},
                "customer_id": {"type": "string"},
            },
            "required": ["amount", "customer_id"],
        },
    },
    {
        "name": "refund_large",
        "description": "Process a large refund",
        "inputSchema": {
            "type": "object",
            "properties": {
                "amount": {"type": "integer"},
                "customer_id": {"type": "string"},
            },
            "required": ["amount", "customer_id"],
        },
    },
    {
        "name": "delete_record",
        "description": "Delete a customer record",
        "inputSchema": {
            "type": "object",
            "properties": {
                "customer_id": {"type": "string"},
            },
            "required": ["customer_id"],
        },
    },
    {
        "name": "send_slack_message",
        "description": "Send a Slack message",
        "inputSchema": {
            "type": "object",
            "properties": {
                "channel": {"type": "string"},
                "message": {"type": "string"},
            },
            "required": ["channel", "message"],
        },
    },
]


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/mcp":
            self.send_error(404)
            return

        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        request = json.loads(body or b"{}")
        request_id = request.get("id")
        method = request.get("method")

        if method == "initialize":
            response = {
                "jsonrpc": "2.0",
                "id": request_id,
                "result": {
                    "protocolVersion": "2025-03-26",
                    "capabilities": {
                        "tools": {
                            "listChanged": False,
                        },
                    },
                    "serverInfo": {
                        "name": "fake-upstream",
                        "version": "1.0.0",
                    },
                },
            }
        elif method == "tools/list":
            response = {
                "jsonrpc": "2.0",
                "id": request_id,
                "result": {
                    "tools": TOOLS,
                },
            }
        else:
            params = request.get("params") or {}
            arguments = params.get("arguments") or {}
            hold_ms = int(arguments.get("hold_ms", 0) or 0)
            if hold_ms > 0:
                time.sleep(hold_ms / 1000.0)
            response = {
                "jsonrpc": "2.0",
                "id": request_id,
                "result": {
                    "content": [
                        {
                            "type": "text",
                            "text": json.dumps(
                                {
                                    "ok": True,
                                    "tool": params.get("name"),
                                    "arguments": arguments,
                                }
                            ),
                        }
                    ],
                    "isError": False,
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
