# khata-tools

Offline tooling for [khata](../README.md). Everything here runs on a laptop, by
hand, and none of it ships to the phone.

- **`eval`** — measures the Go parser against a labelled corpus by driving the
  real binary through `khata parse -batch`, not a Python reimplementation.
- **`synth`** — compiles user corrections into a deterministic rule pack. The
  language model proposes a match expression; the proposal only ships if it
  reproduces the user's own decisions exactly.
- **`report`** — builds a monthly markdown report straight from the ledger file,
  including recurring-charge detection.

```sh
pip install -e '.[dev]'
python -m pytest -q

khata-tools eval
khata-tools synth corrections.json -o pack.json
khata-tools report ~/.local/share/khata/ledger.jsonl
```

The `heuristic` provider is the default and needs no model, no network and no
configuration. `ollama` and `anthropic` are opt-in, and all three go through the
same validation gate in `rulesynth/synth.py`.
