from mcp.client.streamable_http import streamable_http_client
from mcp import ClientSession
from flask import Flask, request, jsonify
import os
import asyncio

app = Flask(__name__)
GATEWAY_URL = os.environ["GATEWAY_URL"]

DISPATCH = {
    "lookup-charge":  ("list_recent_charges",  {"limit": 1}),
    "create-refund":  ("create_refund",         {"charge_or_pi": "ch_fake_001", "reason": "requested_by_customer"}),
    "deny-test":      ("delete_customer",       {"customer_id": "cust_001"}),
    "pii-message":    ("send_slack_message",    {"channel": "#support", "message": "Customer SSN: 123-45-6789"}),
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
            try:
                await session.call_tool(tool, args)
            except Exception:
                # Gateway may deny via policy; eval-runner inspects audit_log
                pass
            return session_id


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8085)
