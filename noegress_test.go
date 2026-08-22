package khata

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// "Your data never leaves your device" is the entire premise of this project,
// and a promise in a README is worth nothing. This test makes it a build
// failure.
//
// It parses every non-test Go file that ships in the engine and fails if any
// of them imports a package capable of opening a socket, shelling out, or
// otherwise moving bytes off the machine. A contributor who adds a crash
// reporter, an analytics SDK or a "just a quick version check" HTTP call finds
// out in CI, in a test whose failure message says why the rule exists.
//
// Scope: this covers the Go engine — the code that runs on the user's phone and
// laptop and touches their transactions. It deliberately does not cover
// toolchain/, which is Python, runs off-device, is invoked by hand, and may
// call a language model provider when the user opts in.
var forbiddenImports = map[string]string{
	"net":              "opens sockets",
	"net/http":         "makes HTTP requests",
	"net/url":          "usually arrives alongside an HTTP client", // cheap to avoid, and a useful tripwire
	"net/rpc":          "makes remote calls",
	"net/smtp":         "sends mail",
	"os/exec":          "can shell out to a network tool such as curl",
	"database/sql":     "implies a database driver, which may be a network client",
	"log/syslog":       "can forward logs to a remote collector",
	"crypto/tls":       "only needed to talk to something remote",
	"golang.org/x/net": "network primitives",
}

// scanRoots are the directories whose contents ship in the on-device engine.
var scanRoots = []string{"core", "cmd", "."}

func TestEngineHasNoNetworkImports(t *testing.T) {
	seen := map[string]bool{}

	for _, root := range scanRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Do not descend out of the Go tree.
				switch d.Name() {
				case ".git", "android", "toolchain", "docs", "testdata":
					if path != "." {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true

			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			for _, imp := range f.Imports {
				checkImport(t, path, imp)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(seen) == 0 {
		t.Fatal("scanned no Go files; the walk roots are wrong and this test is not actually checking anything")
	}
	t.Logf("verified %d Go source files carry no network imports", len(seen))
}

func checkImport(t *testing.T, path string, imp *ast.ImportSpec) {
	t.Helper()
	p, err := strconv.Unquote(imp.Path.Value)
	if err != nil {
		return
	}
	if reason, bad := forbiddenImports[p]; bad {
		t.Errorf(`%s imports %q, which %s.

khata's premise is that financial data never leaves the device, and this test
is what keeps that true. If this import is genuinely necessary, that is a
change to the project's guarantee, not a test to silence: raise it in an issue
first, and update the README's privacy section in the same pull request.`,
			path, p, reason)
		return
	}
	// Catch third-party clients too: anything under a vendor path that looks
	// like an SDK is worth a human deciding on.
	for _, marker := range []string{"grpc", "aws-sdk", "firebase", "sentry", "analytics", "telemetry"} {
		if strings.Contains(p, marker) {
			t.Errorf("%s imports %q, which looks like a remote-service client; see the note in noegress_test.go", path, p)
		}
	}
}

// The engine must also carry no third-party dependencies at all. That is a
// stronger claim than "no network imports" and it is what makes the previous
// test trustworthy: a dependency could open a socket without this module
// naming a net package anywhere.
func TestEngineHasNoThirdPartyDependencies(t *testing.T) {
	// An absent go.sum is the expected, passing case: nothing to verify because
	// nothing is depended on.
	fi, err := os.Stat("go.sum")
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 0 {
		t.Errorf(`go.sum is non-empty (%d bytes), so this module has third-party dependencies.

The engine is meant to depend on the Go standard library only. Every dependency
is code that runs next to someone's bank messages, and one a reviewer has to
audit before they can believe the privacy claim at all.`, fi.Size())
	}
}
