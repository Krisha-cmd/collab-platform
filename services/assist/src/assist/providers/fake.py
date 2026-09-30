"""A provider with fixed, predictable answers.

Used for tests and for working without an API key or network. It only knows
the tasks the service asks for; it is not a language model.
"""

import asyncio
import json
import re
from collections.abc import AsyncIterator

from .base import Messages, Provider


def _text_between_tags(content: str) -> str:
    match = re.search(r"<text>(.*?)</text>", content, flags=re.DOTALL)
    return match.group(1) if match else content


def _first_sentence(text: str) -> str:
    match = re.search(r".+?[.!?](?=\s|$)", text.strip(), flags=re.DOTALL)
    return (match.group(0) if match else text.strip())[:200]


class FakeProvider(Provider):
    async def complete(self, task, messages, *, json_mode=False, fast=False, timeout=None):
        text = _text_between_tags(messages[-1]["content"])

        if task == "grammar":
            # One rule: a lone lowercase "i" should be "I". Like a real model is
            # asked to, include a neighbouring word so `original` is unique.
            fixes = []
            for match in re.finditer(r"(?:\S+ )?\bi\b(?: \S+)?", text):
                original = match.group(0)
                replacement = re.sub(r"\bi\b", "I", original)
                fixes.append(
                    {
                        "original": original,
                        "replacement": replacement,
                        "reason": "Capitalize the pronoun 'I'",
                        "kind": "grammar",
                    }
                )
            return json.dumps({"fixes": fixes})

        if task == "summarize_chunk":
            return _first_sentence(text)

        if task == "enhance":
            # "Concise" in the simplest possible way: drop the word "very".
            return re.sub(r"\bvery ", "", text)

        return "(fake answer)"

    async def stream(self, task, messages, *, fast=False, timeout=None) -> AsyncIterator[str]:
        if task == "summarize":
            text = _text_between_tags(messages[-1]["content"])
            words = ("Summary: " + _first_sentence(text)).split(" ")
        else:
            words = ["and", "then", "we", "shipped", "it."]
        for i, word in enumerate(words):
            await asyncio.sleep(0.02)
            yield word if (task == "summarize" and i == 0) else " " + word