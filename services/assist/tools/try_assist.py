"""Calls the running Assist service and prints the results.

    python services\\assist\\tools\\try_assist.py
    python services\\assist\\tools\\try_assist.py "Their going too the libary tomorow."
"""

import asyncio
import sys
import uuid

import grpc

from collab.v1 import assist_pb2, assist_pb2_grpc, common_pb2

DEFAULT_TEXT = "yesterday i said i would fix it, but their was to many bugs"


async def main(text: str) -> None:
    async with grpc.aio.insecure_channel("localhost:50061") as channel:
        stub = assist_pb2_grpc.AssistServiceStub(channel)

        print(f"Text: {text!r}\n")
        try:
            reply = await stub.CheckGrammar(
                assist_pb2.CheckGrammarRequest(
                    context=assist_pb2.TextContext(
                        request_id=str(uuid.uuid4()),
                        anchor=common_pb2.TextAnchor(
                            doc_id="demo",
                            revision=7,
                            range=common_pb2.TextRange(start=0, end=len(text)),
                        ),
                        text=text,
                    ),
                ),
                timeout=30,
            )
            print(f"CheckGrammar: {len(reply.suggestions)} suggestion(s)")
            for s in reply.suggestions:
                r = s.anchor.range
                kind = assist_pb2.SuggestionKind.Name(s.kind).removeprefix("SUGGESTION_KIND_")
                print(f"  [{r.start}:{r.end}] {s.original!r} -> {s.replacement!r}  ({kind}: {s.reason})")
        except grpc.aio.AioRpcError as err:
            print(f"CheckGrammar failed: {err.code().name}: {err.details()}")

        print("\nAutocomplete:", end="", flush=True)
        try:
            stream = stub.Autocomplete(
                assist_pb2.AutocompleteRequest(
                    context=assist_pb2.TextContext(
                        request_id=str(uuid.uuid4()),
                        before="Our team meeting is on Friday, so before then",
                    )
                ),
                timeout=30,
            )
            async for chunk in stream:
                print(chunk.delta, end="", flush=True)
            print()
        except grpc.aio.AioRpcError as err:
            print(f"\nAutocomplete failed: {err.code().name}: {err.details()}")


if __name__ == "__main__":
    asyncio.run(main(sys.argv[1] if len(sys.argv) > 1 else DEFAULT_TEXT))