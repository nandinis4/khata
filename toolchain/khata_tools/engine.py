"""Thin wrapper around the compiled Go engine.

The toolchain never reimplements parsing. Measuring a Python copy of the parser
would produce numbers that say nothing about what actually runs on the phone,
and the copy would drift the first time someone fixed a regex on one side only.
Instead the harness shells out to `khata parse -batch`, which is the same code
path the Android app calls through gomobile.
"""

from __future__ import annotations

import json
import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterable


class EngineNotFound(RuntimeError):
    """The khata binary could not be located or built."""


@dataclass(frozen=True)
class ParseResult:
    """One line of `khata parse -batch` output."""

    input: str
    bookable: bool
    kind: str
    reason: str = ""
    amount_minor: int = 0
    direction: str = ""
    channel: str = ""
    merchant_key: str = ""
    account_hint: str = ""
    reference: str = ""
    category: str = ""
    rule_id: str = ""
    confidence: float = 0.0
    needs_review: bool = False
    occurred_at: str = ""

    @classmethod
    def from_json(cls, obj: dict) -> "ParseResult":
        known = {f for f in cls.__dataclass_fields__}  # type: ignore[attr-defined]
        return cls(**{k: v for k, v in obj.items() if k in known})


def repo_root(start: Path | None = None) -> Path:
    """Walk up from *start* until the directory containing go.mod is found."""
    here = (start or Path(__file__).resolve()).resolve()
    for candidate in [here, *here.parents]:
        if (candidate / "go.mod").is_file():
            return candidate
    raise EngineNotFound("could not locate the repository root (no go.mod found above this file)")


@dataclass
class Engine:
    """Runs the khata binary.

    A prebuilt binary is used when one is on PATH or passed explicitly;
    otherwise the wrapper falls back to `go run`, which keeps `pytest` working
    in a fresh clone with no build step.
    """

    binary: str | None = None
    root: Path = field(default_factory=repo_root)

    def _command(self) -> list[str]:
        if self.binary:
            # Resolved against the caller's cwd, not the repo root the
            # subprocess will run in, so a relative --binary works as typed.
            return [str(Path(self.binary).resolve())]
        found = shutil.which("khata")
        if found:
            return [found]
        if shutil.which("go"):
            return ["go", "run", "./cmd/khata"]
        raise EngineNotFound(
            "no khata binary on PATH and no Go toolchain to build one. "
            "Build it with: go build -o khata ./cmd/khata"
        )

    def parse_batch(self, messages: Iterable[str]) -> list[ParseResult]:
        """Parse many messages in a single subprocess.

        Messages are newline-delimited on the wire, so any embedded newlines are
        collapsed first — a multi-line notification is a single logical message
        and must stay on one line.
        """
        payload = "\n".join(" ".join(m.split()) for m in messages)
        if not payload.strip():
            return []

        proc = subprocess.run(
            [*self._command(), "parse", "-batch"],
            input=payload,
            capture_output=True,
            text=True,
            cwd=self.root,
            check=False,
        )
        if proc.returncode != 0:
            raise EngineNotFound(f"khata parse -batch failed:\n{proc.stderr.strip()}")

        results = []
        for line in proc.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            results.append(ParseResult.from_json(json.loads(line)))
        return results

    def parse_one(self, message: str) -> ParseResult:
        out = self.parse_batch([message])
        if not out:
            raise EngineNotFound("engine returned no result for a non-empty message")
        return out[0]
