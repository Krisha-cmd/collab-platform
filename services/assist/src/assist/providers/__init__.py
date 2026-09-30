from ..config import Settings
from .base import Provider, ProviderError, ProviderTimeout, RateLimited


def make_provider(settings: Settings) -> Provider:
    if settings.provider == "fake":
        from .fake import FakeProvider

        return FakeProvider()
    if settings.provider == "openai_compat":
        from .openai_compat import OpenAICompatProvider

        return OpenAICompatProvider(settings)
    raise ValueError(f"Unknown ASSIST_PROVIDER: {settings.provider!r}")


__all__ = ["Provider", "ProviderError", "ProviderTimeout", "RateLimited", "make_provider"]