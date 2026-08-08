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

// Main runs one invocation and returns the process exit status.
//
// avroc-gen-go is an executable, not a server: it is handed a descriptor and an
// output directory on its command line, writes files, and exits. The Unix
// listener and the gRPC server it used to stand up went with the socket
// rendezvous that required them (#114), and the streaming emission that
// outlived them went with #121 — Generate writes whole files through
// plugin.FileWriter, and nothing here names the Generator service any more.
//
// The declared option vocabulary is the one Generate reads, and the two are
// checked against each other by TestDeclaredOptionsAreTheOnesGenerateReads: a
// key declared here and ignored there would be a manifest line avroc lets
// through and the generator silently drops.
func Main(ctx context.Context, c cli.Context) int {
	return plugin.Main(ctx, c, plugin.NewInfo("go", declaredOptions()...), Generate)
}

// declaredOptions is the --opt vocabulary avroc-gen-go declares, in the order
// the declaration lists it.
//
// A function rather than a variable so that nothing can append to it, and named
// so that the test above has something to range over: a key added here and never
// wired into Generate is exactly the failure the declaration is supposed to make
// impossible.
func declaredOptions() []string {
	return []string{"encoding", "package_name"}
}
