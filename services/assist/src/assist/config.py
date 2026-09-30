"""Settings for the Assist service, read from environment variables or .env."""

import os
from dataclasses import dataclass

from dotenv import find_dotenv, load_dotenv

# Look for a .env file starting from the folder you run the command in
# (the repo root) and walking upwards.
load_dotenv(find_dotenv(usecwd=True))


@dataclass(frozen=True)
class Settings:
    provider: str  # "fake" or "openai_compat"
    base_url: str
    api_key: str
    model: str  # used for grammar, summaries and enhancement
    fast_model: str  # used for autocomplete, where speed matters most
    reasoning_effort: str | None  # "low" / "medium" / "high", or None to omit
    port: int
    max_input_chars: int  # longest text accepted in one request
    max_summary_chars: int  # longest document accepted for summarizing
    default_timeout_s: float  # used when the caller sets no gRPC deadline
    @staticmethod
    def from_env() -> "Settings":
        model = os.getenv("ASSIST_MODEL", "")
        return Settings(
            provider=os.getenv("ASSIST_PROVIDER", "fake").strip().lower(),
            base_url=os.getenv("ASSIST_BASE_URL", ""),
            api_key=os.getenv("ASSIST_API_KEY", ""),
            model=model,
            fast_model=os.getenv("ASSIST_FAST_MODEL") or model,
            reasoning_effort=os.getenv("ASSIST_REASONING_EFFORT") or None,
            port=int(os.getenv("ASSIST_PORT", "50061")),
            max_input_chars=int(os.getenv("ASSIST_MAX_INPUT_CHARS", "8000")),
            max_summary_chars=int(os.getenv("ASSIST_MAX_SUMMARY_CHARS", "60000")),
            default_timeout_s=float(os.getenv("ASSIST_DEFAULT_TIMEOUT_S", "20")),
        )