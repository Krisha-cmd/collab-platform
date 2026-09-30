import asyncio

import pytest

from assist.providers.base import Provider
from assist.providers.fake import FakeProvider
from assist.tasks.enhance import InvalidMode, enhance
from assist.tasks.summarize import split_into_chunks, summarize
from collab.v1 import assist_pb2, common_pb2


class RecordingProvider(Provider):
    """Remembers every call; answers with fixed text."""

    def __init__(self, answer: str = "ok"):
        self.answer = answer
        self.calls: list[tuple[str, str]] = []

    async def complete(self, task, messages, **kwargs):
        self.calls.append((task, messages[-1]["content"]))
        return self.answer

    async def stream(self, task, messages, **kwargs):
        self.calls.append((task, messages[-1]["content"]))
        yield "final"


def run_summary(provider, text, length=assist_pb2.SUMMARY_LENGTH_UNSPECIFIED):
    request = assist_pb2.SummarizeRequest(
        request_id="s1",
        document=common_pb2.DocumentSnapshot(doc_id="d", revision=42, text=text),
        length=length,
    )

    async def collect():
        return [r async for r in summarize(provider, request, None)]

    return asyncio.run(collect())


# --- chunking -----------------------------------------------------------------

def test_short_text_is_one_chunk():
    assert split_into_chunks("one\n\ntwo", max_chars=100) == ["one\n\ntwo"]


def test_paragraphs_are_packed_without_exceeding_limit():
    chunks = split_into_chunks("\n\n".join(["x" * 40] * 5), max_chars=100)
    assert len(chunks) == 3 and all(len(c) <= 100 for c in chunks)


def test_huge_paragraph_is_cut():
    chunks = split_into_chunks("y" * 250, max_chars=100)
    assert [len(c) for c in chunks] == [100, 100, 50]


def test_nothing_is_lost():
    text = "\n\n".join(f"para {i} " + "z" * 30 for i in range(20))
    joined = "\n\n".join(split_into_chunks(text, max_chars=120))
    assert joined == text


# --- summarize ------------------------------------------------------------------

def test_short_document_uses_one_streamed_call():
    provider = RecordingProvider()
    replies = run_summary(provider, "Short notes. Nothing else.")
    assert [task for task, _ in provider.calls] == ["summarize"]
    assert replies[0].revision == 42 and replies[0].request_id == "s1"


def test_long_document_is_mapped_then_reduced():
    provider = RecordingProvider(answer="chunk summary")
    long_text = "\n\n".join("Paragraph. " + "w" * 3000 for _ in range(4))
    run_summary(provider, long_text)
    tasks = [task for task, _ in provider.calls]
    assert tasks[:-1] == ["summarize_chunk"] * (len(tasks) - 1) and len(tasks) > 2
    assert tasks[-1] == "summarize"
    assert "Part 1:" in provider.calls[-1][1]


def test_empty_document_gives_no_output():
    assert run_summary(RecordingProvider(), "   ") == []


def test_fake_summary_streams_text():
    replies = run_summary(FakeProvider(), "We chose Raft. It is simpler.")
    assert "".join(r.delta for r in replies) == "Summary: We chose Raft."


# --- enhance --------------------------------------------------------------------

def enhance_request(text, mode=assist_pb2.ENHANCE_MODE_CONCISE, start=0):
    return assist_pb2.EnhanceRequest(
        context=assist_pb2.TextContext(
            request_id="e1",
            anchor=common_pb2.TextAnchor(
                doc_id="d", revision=5, range=common_pb2.TextRange(start=start, end=start + len(text))
            ),
            text=text,
        ),
        mode=mode,
    )


def test_enhance_narrows_to_changed_words():
    reply = asyncio.run(enhance(FakeProvider(), enhance_request("It was very good work.", start=50), None))
    [s] = reply.suggestions
    assert (s.original, s.replacement) == ("very good", "good")
    assert (s.anchor.range.start, s.anchor.range.end) == (57, 66)
    assert s.kind == assist_pb2.SUGGESTION_KIND_ENHANCEMENT


def test_enhance_strips_quotes_and_keeps_surrounding_spaces():
    provider = RecordingProvider(answer='"Great result."')
    reply = asyncio.run(enhance(provider, enhance_request(" good result. "), None))
    [s] = reply.suggestions
    assert (s.original, s.replacement) == ("good", "Great")  # narrowed to the changed word
    assert (s.anchor.range.start, s.anchor.range.end) == (1, 5)  # leading space untouched


def test_unchanged_rewrite_gives_no_suggestion():
    provider = RecordingProvider(answer="Already fine.")
    assert list(asyncio.run(enhance(provider, enhance_request("Already fine."), None)).suggestions) == []


def test_mode_must_be_set():
    with pytest.raises(InvalidMode):
        asyncio.run(
            enhance(FakeProvider(), enhance_request("x", mode=assist_pb2.ENHANCE_MODE_UNSPECIFIED), None)
        )