from mcp.client.streamable_http import streamable_http_client
from mcp import ClientSession
from flask import Flask, request, jsonify
import os
import asyncio

app = Flask(__name__)
GATEWAY_URL = os.environ["GATEWAY_URL"]

DISPATCH = {
    "small-refund":      ("refund_small",       {"amount": 50, "customer_id": "cust_001"}),
    "large-refund":      ("refund_large",        {"amount": 12000, "customer_id": "cust_002"}),
    "delete-customer":   ("delete_record",       {"customer_id": "cust_003"}),
    "slack-pii-message": ("send_slack_message",  {"channel": "#support", "message": "Customer SSN: 123-45-6789"}),
}


@app.post("/trigger")
def trigger():
    input_key = (request.get_json(silent=True) or {}).get("input", "")
    if input_key not in DISPATCH:
        return jsonify({"error": "unknown input"}), 400
    tool, args = DISPATCH[input_key]
    session_id = asyncio.run(_call_tool(tool, args))
    return jsonify({"session_id": session_id})


async def _call_tool(tool: str, args: dict) -> str:
    async with streamable_http_client(GATEWAY_URL) as (read, write, get_session_id):
        async with ClientSession(read, write) as session:
            await session.initialize()
            session_id = get_session_id()
            if not session_id:
                raise RuntimeError("gateway did not provide an MCP session ID")
            await session.call_tool(tool, args)
            return session_id


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8085)
