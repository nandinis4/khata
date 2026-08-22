package rules

import (
	"bytes"
	_ "embed"
	"fmt"
)

//go:embed packs/builtin.json
var builtinPackJSON []byte

// BuiltinPack returns the rule pack shipped with the binary.
//
// Embedding keeps the app self-sufficient: a fresh install categorises
// sensibly on the very first transaction, with no download and no account.
func BuiltinPack() (*Pack, error) {
	p, err := LoadPack(bytes.NewReader(builtinPackJSON))
	if err != nil {
		return nil, fmt.Errorf("builtin pack: %w", err)
	}
	return p, nil
}

// NewDefaultEngine builds an engine from the builtin pack plus any additional
// packs — typically the synthesized pack the toolchain produced from this
// user's own corrections.
func NewDefaultEngine(extra ...*Pack) (*Engine, error) {
	base, err := BuiltinPack()
	if err != nil {
		return nil, err
	}
	return NewEngine(append([]*Pack{base}, extra...)...)
}

// BuiltinPackJSON exposes the raw bytes so the CLI can dump the pack for
// inspection without re-reading it from disk.
func BuiltinPackJSON() []byte { return append([]byte(nil), builtinPackJSON...) }
