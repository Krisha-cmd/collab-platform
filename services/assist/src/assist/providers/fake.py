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

        return "(fake answer)"

    async def stream(self, task, messages, *, fast=False, timeout=None) -> AsyncIterator[str]:
        for word in ["and", "then", "we", "shipped", "it."]:
            await asyncio.sleep(0.05)
            yield " " + word