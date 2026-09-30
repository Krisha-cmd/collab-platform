"""Two simulated users edit one document at the same time.

Shows the conflict rule in action: Bob's edit is rejected because Alice got
there first, Bob rebases and resends, a retry is detected as a duplicate, and
a subscriber sees every committed edit in order.

    python services\\docnode\\tools\\try_docs.py
"""

import asyncio
import uuid

import grpc

from collab.v1 import common_pb2, document_pb2, document_pb2_grpc

DOC = "demo-" + uuid.uuid4().hex[:6]  # a fresh document each run


def insert(client, seq, base, pos, s):
    return common_pb2.Operation(
        op_id=common_pb2.OpId(client_id=client, seq=seq),
        doc_id=DOC,
        base_revision=base,
        changes=[common_pb2.TextChange(range=common_pb2.TextRange(start=pos, end=pos), insert=s)],
    )


def replace(client, seq, base, start, end, s):
    return common_pb2.Operation(
        op_id=common_pb2.OpId(client_id=client, seq=seq),
        doc_id=DOC,
        base_revision=base,
        changes=[common_pb2.TextChange(range=common_pb2.TextRange(start=start, end=end), insert=s)],
    )


def map_position(pos, committed_ops):
    """Moves an insert position past edits it did not know about.

    A tiny version of what CodeMirror's collab package does when it rebases.
    (Positions are UTF-16 units; this demo only uses plain ASCII text.)
    """
    for op in committed_ops:
        for c in op.changes:
            start, end, added = c.range.start, c.range.end, len(c.insert)
            if end <= pos and start < pos:  # the edit is entirely before pos
                pos += added - (end - start)
            elif start < pos < end:  # pos was inside replaced text
                pos = start + added
    return pos


async def watch(stub):
    stream = stub.Subscribe(document_pb2.SubscribeRequest(doc_id=DOC, from_revision=0))
    async for msg in stream:
        op = msg.operation
        c = op.changes[0]
        print(f"    [subscriber] rev {msg.revision}: {op.op_id.client_id} "
              f"replaced [{c.range.start}:{c.range.end}] with {c.insert!r}")


async def submit(stub, *ops):
    return await stub.SubmitOperations(
        document_pb2.SubmitOperationsRequest(doc_id=DOC, operations=ops), timeout=5
    )


async def snapshot(stub):
    reply = await stub.GetSnapshot(document_pb2.GetSnapshotRequest(doc_id=DOC), timeout=5)
    return reply.snapshot


async def main():
    async with grpc.aio.insecure_channel("localhost:50051") as channel:
        stub = document_pb2_grpc.DocumentServiceStub(channel)
        watcher = asyncio.create_task(watch(stub))
        await asyncio.sleep(0.2)

        print(f"Document {DOC}\n")
        print("Alice types 'Hello world'")
        await submit(stub, insert("alice", 1, 0, 0, "Hello world"))
        await asyncio.sleep(0.1)

        print("\nAlice and Bob both see revision 1. At the same time:")
        print("  Alice replaces 'world' with 'team'")
        print("  Bob inserts 'big ' before 'world'")
        await submit(stub, replace("alice", 2, 1, 6, 11, "team"))
        await asyncio.sleep(0.1)

        bob_edit = insert("bob", 1, 1, 6, "big ")
        try:
            await submit(stub, bob_edit)
        except grpc.aio.AioRpcError as err:
            print(f"\nBob's edit was rejected: {err.code().name}: {err.details()}")

        # Bob catches up: fetch what he missed and move his edit past it.
        snap = await snapshot(stub)
        missed = []
        history = stub.Subscribe(document_pb2.SubscribeRequest(doc_id=DOC, from_revision=1))
        async for msg in history:
            missed.append(msg.operation)
            if msg.revision == snap.revision:
                break
        history.cancel()
        new_pos = map_position(6, missed)
        bob_edit = insert("bob", 1, snap.revision, new_pos, "big ")
        print(f"Bob rebases onto revision {snap.revision} (position 6 -> {new_pos}) and resends")
        reply = await submit(stub, bob_edit)
        print(f"  accepted: revision {reply.revision}")
        await asyncio.sleep(0.1)

        print("\nBob's network hiccups and his client sends the same edit again")
        reply = await submit(stub, bob_edit)
        print(f"  applied={reply.applied} duplicates={reply.duplicates} (not applied twice)")

        snap = await snapshot(stub)
        print(f"\nFinal text at revision {snap.revision}: {snap.text!r}")
        watcher.cancel()


if __name__ == "__main__":
    asyncio.run(main())