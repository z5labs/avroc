// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"context"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/plugin"
)

// Main runs one generation and returns the process exit status.
//
// avroc-gen-go is an executable, not a server: it is handed a descriptor and an
// output directory on its command line, writes files, and exits. The Unix
// listener and the gRPC server it used to stand up are gone with the socket
// rendezvous that required them (#114); what remains of the service — the
// streaming emission generatorService still implements internally — is #121's
// to replace and #124's to delete.
func Main(ctx context.Context, c cli.Context) int {
	return plugin.Main(ctx, c, "avroc-gen-go", (&generatorService{}).Generate)
}
