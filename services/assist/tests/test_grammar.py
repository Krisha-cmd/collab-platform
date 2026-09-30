import asyncio

import pytest

from assist.providers.base import Provider, ProviderError
from assist.providers.fake import FakeProvider
from assist.tasks.grammar import check_grammar
from collab.v1 import assist_pb2, common_pb2


def context(text: str, start: int = 0) -> assist_pb2.TextContext:
    return assist_pb2.TextContext(
        request_id="r1",
        anchor=common_pb2.TextAnchor(
            doc_id="d", revision=3, range=common_pb2.TextRange(start=start, end=start + len(text))
        ),
        text=text,
    )


class ScriptedProvider(Provider):
    """Returns the given answers in order, one per call."""

    def __init__(self, *answers: str):
        self.answers = list(answers)
        self.calls = 0

    async def complete(self, task, messages, **kwargs):
        self.calls += 1
        return self.answers.pop(0)

    async def stream(self, task, messages, **kwargs):
        yield ""


def test_fake_provider_end_to_end():
    reply = asyncio.run(check_grammar(FakeProvider(), context("so i went", start=100), None))
    [s] = reply.suggestions
    assert (s.anchor.range.start, s.anchor.range.end) == (103, 104)
    assert (s.original, s.replacement, s.anchor.revision) == ("i", "I", 3)
    assert reply.request_id == "r1"


def test_retries_once_on_bad_json_and_accepts_code_fences():
    provider = ScriptedProvider(
        "sorry, here you go:",
        '```json\n{"fixes": [{"original": "teh cat", "replacement": "the cat", "kind": "spelling"}]}\n```',
    )
    reply = asyncio.run(check_grammar(provider, context("teh cat sat"), None))
    assert provider.calls == 2
    [s] = reply.suggestions
    assert (s.original, s.replacement, s.kind) == (
        "teh",
        "the",
        assist_pb2.SUGGESTION_KIND_SPELLING,
    )


def test_gives_up_after_two_bad_answers():
    with pytest.raises(ProviderError):
        asyncio.run(check_grammar(ScriptedProvider("nope", "still nope"), context("x y"), None))


def test_hallucinated_fix_is_dropped():
    provider = ScriptedProvider('{"fixes": [{"original": "not there", "replacement": "Not there"}]}')
    assert list(asyncio.run(check_grammar(provider, context("fine text"), None)).suggestions) == []