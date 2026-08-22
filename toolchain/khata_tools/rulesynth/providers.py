"""Model providers for rule synthesis.

Three implementations behind one protocol, in ascending order of how much they
ask of the user:

  HeuristicProvider  no model, no network, no configuration. This is the
                     default, and it is genuinely useful: most corrections are
                     unanimous and need generalisation, not reasoning.
  OllamaProvider     a model running on the user's own machine.
  AnthropicProvider  a hosted model, used only when an API key is set.

Whichever runs, its output is treated as a *proposal* that has to survive
validation against the user's actual corrections before it becomes a rule. See
synth.validate_rule.
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from typing import Protocol, runtime_checkable

SYSTEM_PROMPT = """\
You compile a user's expense-categorisation corrections into deterministic \
matching rules for a local budgeting app.

You will be given a category and the list of merchant keys the user assigned to \
it. Merchant keys are already normalised: lowercase, no punctuation, no legal \
suffixes, no city codes.

Propose the SIMPLEST match expression that covers those keys. Prefer a short \
`contains` substring over a regex. Prefer several precise substrings over one \
loose one. A rule that is too broad will silently mis-file unrelated \
transactions, which is worse than no rule at all.

Respond with JSON only, no prose:
{"merchant_contains": ["..."], "merchant_regex": "", "note": "one short sentence"}

Leave merchant_regex empty unless a substring genuinely cannot express the \
pattern. Never invent merchants that are not implied by the keys you were given.\
"""


def build_user_prompt(category: str, merchant_keys: list[str], counter_examples: list[str]) -> str:
    lines = [
        f"Category: {category}",
        "Merchant keys the user assigned to it:",
        *(f"  - {k}" for k in sorted(merchant_keys)),
    ]
    if counter_examples:
        lines += [
            "",
            "Merchant keys assigned to OTHER categories. Your match must not cover any of these:",
            *(f"  - {k}" for k in sorted(counter_examples)),
        ]
    return "\n".join(lines)


@runtime_checkable
class Provider(Protocol):
    name: str

    def propose(self, category: str, merchant_keys: list[str], counter_examples: list[str]) -> dict:
        """Return a dict with merchant_contains / merchant_regex / note."""


@dataclass
class HeuristicProvider:
    """Generalises without a model.

    The strategy is the longest common substring across the corrected merchant
    keys, when that substring is distinctive enough to be safe; otherwise it
    falls back to listing the keys verbatim. Verbatim keys are a perfectly good
    rule — they just do not generalise to a merchant the user has not seen yet.
    """

    name: str = "heuristic"
    min_fragment: int = 4

    def propose(self, category: str, merchant_keys: list[str], counter_examples: list[str]) -> dict:
        keys = sorted(set(merchant_keys))
        if len(keys) == 1:
            return {"merchant_contains": keys, "merchant_regex": "", "note": "single corrected merchant"}

        common = _longest_common_substring(keys)
        safe = (
            len(common) >= self.min_fragment
            and not any(common in c for c in counter_examples)
        )
        if safe:
            return {
                "merchant_contains": [common],
                "merchant_regex": "",
                "note": f"shared fragment across {len(keys)} corrected merchants",
            }
        return {
            "merchant_contains": keys,
            "merchant_regex": "",
            "note": "no safe shared fragment; listing merchants explicitly",
        }


def _longest_common_substring(strings: list[str]) -> str:
    if not strings:
        return ""
    shortest = min(strings, key=len)
    for length in range(len(shortest), 0, -1):
        for start in range(len(shortest) - length + 1):
            candidate = shortest[start : start + length]
            if all(candidate in s for s in strings):
                return candidate.strip()
    return ""


@dataclass
class OllamaProvider:
    """Talks to a model on localhost.

    Kept in the toolchain rather than the app on purpose: this still sends
    merchant names to another process, and the app's guarantee is that nothing
    leaves the device at all. Here the user has explicitly run a command.
    """

    name: str = "ollama"
    model: str = "llama3.1"
    host: str = "http://127.0.0.1:11434"

    def propose(self, category: str, merchant_keys: list[str], counter_examples: list[str]) -> dict:
        import urllib.request  # imported lazily: importing it should not be a side effect of importing the toolchain

        payload = json.dumps(
            {
                "model": self.model,
                "system": SYSTEM_PROMPT,
                "prompt": build_user_prompt(category, merchant_keys, counter_examples),
                "stream": False,
                "format": "json",
            }
        ).encode()
        req = urllib.request.Request(
            f"{self.host}/api/generate", data=payload, headers={"Content-Type": "application/json"}
        )
        with urllib.request.urlopen(req, timeout=120) as resp:
            body = json.loads(resp.read())
        return _coerce(body.get("response", "{}"))


@dataclass
class AnthropicProvider:
    """Hosted model. Requires ANTHROPIC_API_KEY and the `anthropic` extra."""

    name: str = "anthropic"
    model: str = "claude-sonnet-4-5"

    def propose(self, category: str, merchant_keys: list[str], counter_examples: list[str]) -> dict:
        try:
            import anthropic
        except ImportError as exc:  # pragma: no cover - depends on optional extra
            raise RuntimeError("install the extra first:  pip install 'khata-tools[anthropic]'") from exc

        if not os.getenv("ANTHROPIC_API_KEY"):
            raise RuntimeError("ANTHROPIC_API_KEY is not set")

        client = anthropic.Anthropic()
        msg = client.messages.create(
            model=self.model,
            max_tokens=512,
            system=SYSTEM_PROMPT,
            messages=[{"role": "user", "content": build_user_prompt(category, merchant_keys, counter_examples)}],
        )
        text = "".join(block.text for block in msg.content if block.type == "text")
        return _coerce(text)


def _coerce(text: str) -> dict:
    """Parse a model response into the proposal shape, tolerating stray prose."""
    text = text.strip()
    if not text.startswith("{"):
        start, end = text.find("{"), text.rfind("}")
        if start < 0 or end < 0:
            raise ValueError(f"model returned no JSON object: {text[:160]!r}")
        text = text[start : end + 1]
    obj = json.loads(text)
    return {
        "merchant_contains": [str(s).strip().lower() for s in obj.get("merchant_contains", []) if str(s).strip()],
        "merchant_regex": str(obj.get("merchant_regex", "") or ""),
        "note": str(obj.get("note", "") or ""),
    }


def get_provider(name: str) -> Provider:
    match name:
        case "heuristic":
            return HeuristicProvider()
        case "ollama":
            return OllamaProvider()
        case "anthropic":
            return AnthropicProvider()
    raise ValueError(f"unknown provider {name!r}; choose heuristic, ollama or anthropic")
