// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"context"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/plugin"
)

// Main runs one invocation and returns the process exit status.
//
// avroc-gen-pcf is an executable, not a server: it is handed a descriptor and
// an output directory on its command line, writes files, and exits. The Unix
// listener and the gRPC server it used to stand up are gone with the socket
// rendezvous that required them (#114); what remains of the service — the
// streaming emission generatorService still implements internally — is #123's
// to replace and #124's to delete.
//
// It declares an empty option vocabulary rather than none at all. Avro's
// Parsing Canonical Form is defined by the specification and not by this
// generator, so there is nothing here an option could configure without
// producing bytes that no other implementation would agree with.
func Main(ctx context.Context, c cli.Context) int {
	return plugin.Main(ctx, c, plugin.NewInfo("pcf"), (&generatorService{}).Generate)
}
