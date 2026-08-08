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
// listener and the gRPC server it used to stand up are gone with the socket
// rendezvous that required them (#114); what remains of the service — the
// streaming emission generatorService still implements internally — is #122's
// to replace and #124's to delete.
//
// It declares an empty option vocabulary rather than none at all: the Avro JSON
// schema of a resolved schema is not a thing there is anything to configure
// about, so an option in the manifest is a mistake, and declaring the emptiness
// is what lets avroc say so before this generator is run.
func Main(ctx context.Context, c cli.Context) int {
	return plugin.Main(ctx, c, plugin.NewInfo("json"), (&generatorService{}).Generate)
}
