// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenjson

import (
	"context"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/plugin"
)

// Main runs one invocation and returns the process exit status.
//
// avroc-gen-json is an executable, not a server: it is handed a descriptor and
// an output directory on its command line, writes files, and exits. The Unix
// listener and the gRPC server it used to stand up went with the socket
// rendezvous that required them (#114), and the streaming emission that
// outlived them went with #122 — Generate writes whole files through
// plugin.FileWriter, and nothing here names the Generator service any more.
//
// It declares an empty option vocabulary rather than none at all: the Avro JSON
// schema of a resolved schema is not a thing there is anything to configure
// about, so an option in the manifest is a mistake, and declaring the emptiness
// is what lets avroc say so before this generator is run.
//
// The declared vocabulary and the one Generate reads are checked against each
// other by TestDeclaredOptionsAreTheOnesGenerateReads, which for an empty
// declaration is the assertion that no --opt changes the output.
func Main(ctx context.Context, c cli.Context) int {
	return plugin.Main(ctx, c, plugin.NewInfo("json", declaredOptions()...), Generate)
}

// declaredOptions is the --opt vocabulary avroc-gen-json declares: none.
//
// It exists to give that vocabulary one definition with a name on it, so that
// "this generator accepts no options" is something a test can hold Generate
// against — TestDeclaredOptionsAreTheOnesGenerateReads — rather than the
// absence of an argument at the call site, which nothing can assert about.
//
// What the declaration says on the wire is present-and-empty: "I accept none",
// which avroc can reject a stray manifest option against, rather than absent,
// which tells avroc to pass the options through and let the generator decide.
// Those are opposite instructions and this generator means the first — but
// plugin.NewInfo's normalizeOptions is what guarantees it, and would do so for
// a nil slice too. Returning an empty one here is agreement with that, not the
// mechanism behind it; TestDeclaredVocabularyIsPresentAndEmpty is what checks
// the mechanism.
func declaredOptions() []string {
	return []string{}
}
