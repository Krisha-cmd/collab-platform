"""Suggests a short continuation of the text at the cursor."""

from collections.abc import AsyncIterator

from collab.v1 import assist_pb2

from ..providers import Provider

SYSTEM_PROMPT = """\
You continue the user's writing. Given the text before the cursor, reply with \
a short, natural continuation: at most one sentence and under 20 words. \
Reply with only the continuation itself, with no quotes and no explanation. \
If the continuation starts a new word, begin it with a space."""

CONTEXT_CHARS = 1500  # how much text before the cursor to send
DEFAULT_MAX_CHARS = 160  # stop streaming after roughly this much output


async def autocomplete(
    provider: Provider, request: assist_pb2.AutocompleteRequest, timeout: float | None
) -> AsyncIterator[assist_pb2.AutocompleteResponse]:
    ctx = request.context
    before = ctx.before[-CONTEXT_CHARS:]
    if not before.strip():
        return

    # max_tokens is applied here, by cutting the stream, rather than passed to
    # the model: "thinking" models spend output tokens on reasoning first, so a
    # small token limit there can end the answer before it starts.
    max_chars = request.max_tokens * 4 if request.max_tokens else DEFAULT_MAX_CHARS

    messages = [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": before},
    ]
    sent = 0
    async for piece in provider.stream("autocomplete", messages, fast=True, timeout=timeout):
        piece = piece[: max_chars - sent]
        if piece:
            sent += len(piece)
            yield assist_pb2.AutocompleteResponse(request_id=ctx.request_id, delta=piece)
        if sent >= max_chars:
            break