"""Calls the running Assist service once per RPC and prints the results."""

import asyncio
import uuid

import grpc

from collab.v1 import assist_pb2, assist_pb2_grpc, common_pb2


async def main() -> None:
    async with grpc.aio.insecure_channel("localhost:50061") as channel:
        stub = assist_pb2_grpc.AssistServiceStub(channel)

        text = "yesterday i said i would fix it"
        reply = await stub.CheckGrammar(
            assist_pb2.CheckGrammarRequest(
                context=assist_pb2.TextContext(
                    request_id=str(uuid.uuid4()),
                    anchor=common_pb2.TextAnchor(
                        doc_id="demo",
                        revision=7,
                        range=common_pb2.TextRange(start=100, end=100 + len(text)),
                    ),
                    text=text,
                ),
            ),
            timeout=5,
        )
        print("CheckGrammar ->")
        print(reply)

        print("Autocomplete (streamed) ->", end="", flush=True)
        stream = stub.Autocomplete(
            assist_pb2.AutocompleteRequest(
                context=assist_pb2.TextContext(request_id=str(uuid.uuid4()))
            ),
            timeout=5,
        )
        async for chunk in stream:
            print(chunk.delta, end="", flush=True)
        print()

        try:
            await stub.Summarize(assist_pb2.SummarizeRequest()).read()
        except grpc.aio.AioRpcError as err:
            print("Summarize ->", err.code().name)


if __name__ == "__main__":
    asyncio.run(main())