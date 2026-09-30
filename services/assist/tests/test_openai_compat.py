"""Checks the requests we send and how we read replies, without a network."""

import asyncio
import json

import httpx
import pytest

from assist.config import Settings
from assist.providers.base import RateLimited
from assist.providers.openai_compat import OpenAICompatProvider

SETTINGS = Settings(
    provider="openai_compat",
    base_url="https://llm.example/v1/",
    api_key="test-key",
    model="big-model",
    fast_model="small-model",
    reasoning_effort="low",
    port=0,
    max_input_chars=1000,
    default_timeout_s=5,
)


def provider_with(handler) -> OpenAICompatProvider:
    client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    return OpenAICompatProvider(SETTINGS, http_client=client)


def test_complete_sends_json_mode_and_reads_answer():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen.update(json.loads(request.content))
        seen["auth"] = request.headers["authorization"]
        return httpx.Response(
            200,
            json={
                "id": "x", "object": "chat.completion", "created": 0, "model": "big-model",
                "choices": [{"index": 0, "finish_reason": "stop",
                             "message": {"role": "assistant", "content": '{"fixes": []}'}}],
            },
        )

    answer = asyncio.run(
        provider_with(handler).complete("grammar", [{"role": "user", "content": "hi"}], json_mode=True)
    )
    assert answer == '{"fixes": []}'
    assert seen["model"] == "big-model"
    assert seen["response_format"] == {"type": "json_object"}
    assert seen["reasoning_effort"] == "low"
    assert seen["auth"] == "Bearer test-key"


def test_stream_uses_fast_model_and_yields_pieces():
    def handler(request: httpx.Request) -> httpx.Response:
        assert json.loads(request.content)["model"] == "small-model"
        events = "".join(
            "data: " + json.dumps({
                "id": "x", "object": "chat.completion.chunk", "created": 0, "model": "small-model",
                "choices": [{"index": 0, "delta": {"content": piece}, "finish_reason": None}],
            }) + "\n\n"
            for piece in [" and", " then"]
        ) + "data: [DONE]\n\n"
        return httpx.Response(200, text=events, headers={"content-type": "text/event-stream"})

    async def collect():
        return [p async for p in provider_with(handler).stream("autocomplete", [], fast=True)]

    assert asyncio.run(collect()) == [" and", " then"]


def test_rate_limit_becomes_rate_limited():
    def handler(request):
        return httpx.Response(429, json={"error": {"message": "quota exceeded"}})

    with pytest.raises(RateLimited):
        asyncio.run(provider_with(handler).complete("grammar", []))