"""Assist (LLM) gRPC service."""

import asyncio
import logging

import grpc

from collab.v1 import assist_pb2_grpc

from .anchoring import utf16_len
from .config import Settings
from .providers import Provider, ProviderError, ProviderTimeout, RateLimited, make_provider
from .tasks.autocomplete import autocomplete
from .tasks.enhance import InvalidMode, enhance
from .tasks.grammar import check_grammar
from .tasks.summarize import summarize

log = logging.getLogger("assist")


class AssistService(assist_pb2_grpc.AssistServiceServicer):
    def __init__(self, provider: Provider, settings: Settings):
        self._provider = provider
        self._settings = settings

    def _timeout(self, context, default: float | None = None) -> float:
        """Time left before the caller's deadline, or the default if none."""
        default = default or self._settings.default_timeout_s
        remaining = context.time_remaining()
        if remaining is None:
            return default
        return max(0.1, min(remaining, default))

    async def _check_size(self, text: str, limit: int, context) -> None:
        if utf16_len(text) > limit:
            await context.abort(
                grpc.StatusCode.INVALID_ARGUMENT, f"text is longer than {limit} characters"
            )

    async def CheckGrammar(self, request, context):
        await self._check_size(request.context.text, self._settings.max_input_chars, context)
        try:
            return await check_grammar(self._provider, request.context, self._timeout(context))
        except ProviderError as err:
            await _abort_for(err, context)

    async def Autocomplete(self, request, context):
        await self._check_size(request.context.before, self._settings.max_input_chars, context)
        try:
            async for chunk in autocomplete(self._provider, request, self._timeout(context)):
                yield chunk
        except ProviderError as err:
            await _abort_for(err, context)

    async def Summarize(self, request, context):
        await self._check_size(request.document.text, self._settings.max_summary_chars, context)
        # Long documents need several model calls, so allow more time by default.
        timeout = self._timeout(context, default=self._settings.default_timeout_s * 3)
        try:
            async for chunk in summarize(self._provider, request, timeout):
                yield chunk
        except ProviderError as err:
            await _abort_for(err, context)

    async def Enhance(self, request, context):
        await self._check_size(request.context.text, self._settings.max_input_chars, context)
        try:
            return await enhance(self._provider, request, self._timeout(context))
        except InvalidMode as err:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(err))
        except ProviderError as err:
            await _abort_for(err, context)


async def _abort_for(err: ProviderError, context) -> None:
    """Reports a provider failure to the caller with a meaningful status code."""
    if isinstance(err, RateLimited):
        code = grpc.StatusCode.RESOURCE_EXHAUSTED
    elif isinstance(err, ProviderTimeout):
        code = grpc.StatusCode.DEADLINE_EXCEEDED
    else:
        code = grpc.StatusCode.UNAVAILABLE
    log.warning("provider error (%s): %s", code.name, err)
    await context.abort(code, f"LLM provider error: {err}")


async def serve() -> None:
    settings = Settings.from_env()
    provider = make_provider(settings)

    server = grpc.aio.server()
    assist_pb2_grpc.add_AssistServiceServicer_to_server(
        AssistService(provider, settings), server
    )
    server.add_insecure_port(f"[::]:{settings.port}")
    await server.start()
    log.info(
        "Assist service on port %d (provider=%s, model=%s)",
        settings.port,
        settings.provider,
        settings.model or "-",
    )
    await server.wait_for_termination()


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    asyncio.run(serve())