"""Offline tooling for khata.

Three things live here, all of them run by hand on a laptop rather than on the
phone:

  evals      measure the Go parser against a labelled corpus
  rulesynth  compile user corrections into a verified deterministic rule pack
  report     build a monthly insight report from the ledger file

The split matters. The Android app is the part that must never touch a network,
and it has a test that enforces it. This package is the part that may — when the
user explicitly runs it, and only with a provider they chose.
"""

__version__ = "0.1.0"
