"""Assist (LLM) gRPC service. For now a fake that needs no API key."""

import asyncio
import logging

import grpc

from collab.v1 import assist_pb2, assist_pb2_grpc, common_pb2

PORT = 50061


def utf16_len(s: str) -> int:
    """Length of s in UTF-16 code units: the unit all positions use."""
    return len(s.encode("utf-16-le")) // 2


class AssistService(assist_pb2_grpc.AssistServiceServicer):
    async def CheckGrammar(self, request, context):
        ctx = request.context
        text = ctx.text
        suggestions = []

        # Fake rule until the LLM is wired in: a lone "i" should be "I".
        words_start = 0
        for word in text.split(" "):
            if word == "i":
                # Convert the Python index to a UTF-16 document position.
                start = ctx.anchor.range.start + utf16_len(text[:words_start])
                suggestions.append(
                    assist_pb2.Suggestion(
                        anchor=common_pb2.TextAnchor(
                            doc_id=ctx.anchor.doc_id,
                            revision=ctx.anchor.revision,
                            range=common_pb2.TextRange(start=start, end=start + 1),
                        ),
                        original="i",
                        replacement="I",
                        reason="Capitalize the pronoun 'I'",
                        kind=assist_pb2.SUGGESTION_KIND_GRAMMAR,
                    )
                )
            words_start += len(word) + 1

        return assist_pb2.CheckGrammarResponse(
            request_id=ctx.request_id, suggestions=suggestions
        )

    async def Autocomplete(self, request, context):
        # Fake streaming: send a canned completion one word at a time.
        for word in ["and", "then", "we", "shipped", "it."]:
            yield assist_pb2.AutocompleteResponse(
                request_id=request.context.request_id, delta=" " + word
            )
            await asyncio.sleep(0.1)

    # Summarize and Enhance are not overridden yet, so gRPC answers
    # them with status UNIMPLEMENTED automatically.


async def serve() -> None:
    server = grpc.aio.server()
    assist_pb2_grpc.add_AssistServiceServicer_to_server(AssistService(), server)
    server.add_insecure_port(f"[::]:{PORT}")
    await server.start()
    logging.info("Assist service listening on port %d", PORT)
    await server.wait_for_termination()


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    asyncio.run(serve())