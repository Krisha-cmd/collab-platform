"""The interface every LLM provider implements.

The rest of the service only talks to this interface, so switching between
the fake provider and a real one is a configuration change, not a code change.
"""

from abc import ABC, abstractmethod
from collections.abc import AsyncIterator

# A chat conversation in the common OpenAI-style format:
# [{"role": "system", "content": "..."}, {"role": "user", "content": "..."}]
Messages = list[dict[str, str]]


class ProviderError(Exception):
    """The LLM call failed. The service reports this as UNAVAILABLE."""


class RateLimited(ProviderError):
    """The provider refused because a rate limit or quota was hit."""


class ProviderTimeout(ProviderError):
    """The provider did not answer in time."""


class Provider(ABC):
    @abstractmethod
    async def complete(
        self,
        task: str,
        messages: Messages,
        *,
        json_mode: bool = False,
        fast: bool = False,
        timeout: float | None = None,
    ) -> str:
        """Returns the model's whole answer as one string.

        task:      a short name ("grammar", "summarize", ...) for logs and fakes.
        json_mode: ask the provider to return a JSON object.
        fast:      use the faster (usually smaller) model.
        """

    @abstractmethod
    def stream(
        self,
        task: str,
        messages: Messages,
        *,
        fast: bool = False,
        timeout: float | None = None,
    ) -> AsyncIterator[str]:
        """Yields the model's answer in pieces as they are generated."""