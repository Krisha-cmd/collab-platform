"""A provider for any service with an OpenAI-compatible chat API.

That includes Google Gemini, Groq, OpenRouter and OpenAI itself: only the
base URL, API key and model name change, and those come from settings.
"""

from collections.abc import AsyncIterator

import httpx
import openai

from ..config import Settings
from .base import Messages, Provider, ProviderError, ProviderTimeout, RateLimited


class OpenAICompatProvider(Provider):
    def __init__(self, settings: Settings, http_client: httpx.AsyncClient | None = None):
        if not (settings.base_url and settings.api_key and settings.model):
            raise ValueError(
                "ASSIST_BASE_URL, ASSIST_API_KEY and ASSIST_MODEL must all be set "
                "when ASSIST_PROVIDER=openai_compat"
            )
        self._client = openai.AsyncOpenAI(
            api_key=settings.api_key,
            base_url=settings.base_url,
            max_retries=1,  # one quick retry on network blips; callers have deadlines
            http_client=http_client,
        )
        self._model = settings.model
        self._fast_model = settings.fast_model
        self._reasoning_effort = settings.reasoning_effort

    def _base_args(self, messages: Messages, fast: bool) -> dict:
        args = {"model": self._fast_model if fast else self._model, "messages": messages}
        if self._reasoning_effort:
            args["reasoning_effort"] = self._reasoning_effort
        return args

    async def complete(self, task, messages, *, json_mode=False, fast=False, timeout=None):
        args = self._base_args(messages, fast)
        if json_mode:
            args["response_format"] = {"type": "json_object"}
        try:
            response = await self._client.chat.completions.create(**args, timeout=timeout)
        except Exception as err:
            raise _translate(err) from err
        return response.choices[0].message.content or ""

    async def stream(self, task, messages, *, fast=False, timeout=None) -> AsyncIterator[str]:
        args = self._base_args(messages, fast)
        try:
            response = await self._client.chat.completions.create(
                **args, stream=True, timeout=timeout
            )
            async for chunk in response:
                if chunk.choices and chunk.choices[0].delta.content:
                    yield chunk.choices[0].delta.content
        except Exception as err:
            raise _translate(err) from err


def _translate(err: Exception) -> Exception:
    """Turns SDK-specific errors into the provider-neutral ones in base.py."""
    if isinstance(err, openai.RateLimitError):
        return RateLimited(str(err))
    if isinstance(err, openai.APITimeoutError):
        return ProviderTimeout(str(err))
    if isinstance(err, openai.OpenAIError):
        return ProviderError(str(err))
    return err  # a bug in our code, not a provider problem: let it surface