"""Connects two browsers' worth of WebSockets to the gateway and prints the traffic.

Needs the document service, the Assist service and the gateway running.

    python -m pip install websockets
    python services\\gateway\\tools\\try_gateway.py
"""

import asyncio
import json
import uuid

import websockets

GATEWAY = "ws://localhost:8080/ws"
DOC = "gw-demo-" + uuid.uuid4().hex[:6]


def short(msg):
    """One readable line per message."""
    kind = next(k for k in msg if k != "requestId")
    body = msg[kind]
    if kind == "presence":
        names = [f"{p['name']}" + (f"@{p['cursor'].get('head', 0)}" if "cursor" in p else "")
                 for p in body.get("participants", [])]
        return f"presence: {', '.join(names)}"
    if kind == "operation":
        c = body["operation"]["changes"][0]
        return f"operation rev {body['revision']} by {body['operation'].get('userId')}: insert {c.get('insert')!r}"
    if kind == "joined":
        return f"joined as {body['name']}; text={body['snapshot'].get('text', '')!r}"
    return f"{kind}: {json.dumps(body)[:100]}"


async def listen(name, ws, log):
    async for raw in ws:
        log.append((name, json.loads(raw)))
        print(f"  [{name}] {short(json.loads(raw))}")


async def main():
    log = []
    async with websockets.connect(f"{GATEWAY}?doc={DOC}&name=Asha") as asha, \
               websockets.connect(f"{GATEWAY}?doc={DOC}&name=Bala") as bala:
        tasks = [asyncio.create_task(listen("Asha", asha, log)),
                 asyncio.create_task(listen("Bala", bala, log))]
        await asyncio.sleep(0.5)

        print("\nAsha types 'Hello' at revision 0")
        await asha.send(json.dumps({"requestId": "e1", "submit": {"operations": [{
            "opId": {"clientId": "asha-tab", "seq": "1"}, "baseRevision": "0",
            "changes": [{"range": {"start": 0, "end": 0}, "insert": "Hello"}]}]}}))
        await asyncio.sleep(0.5)

        print("\nBala, still at revision 0, types too (stale)")
        await bala.send(json.dumps({"requestId": "e2", "submit": {"operations": [{
            "opId": {"clientId": "bala-tab", "seq": "1"}, "baseRevision": "0",
            "changes": [{"range": {"start": 0, "end": 0}, "insert": "Hi"}]}]}}))
        await asyncio.sleep(0.5)

        print("\nBala moves his cursor")
        await bala.send(json.dumps({"cursor": {"revision": "1", "anchor": 5, "head": 5}}))
        await asyncio.sleep(0.5)

        print("\nAsha asks for a grammar check and an autocomplete")
        await asha.send(json.dumps({"requestId": "g1", "checkGrammar": {"context": {
            "requestId": "g1", "text": "so i went",
            "anchor": {"docId": DOC, "revision": "1", "range": {"start": 0, "end": 9}}}}}))
        await asha.send(json.dumps({"requestId": "a1", "autocomplete": {"context": {
            "requestId": "a1", "before": "Hello"}}}))
        await asyncio.sleep(2)

        print("\nBala closes his tab")
        await bala.close()
        await asyncio.sleep(0.5)
        for t in tasks:
            t.cancel()


if __name__ == "__main__":
    asyncio.run(main())