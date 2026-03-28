package main

import (
	"context"
	"fmt"
	"os"

	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/gateway/grpcclient"

	"github.com/dayjaby/daeg/internal/frontend"
)

func main() {
	// RunFromEnvironment connects to BuildKit over gRPC using the socket
	// and credentials BuildKit injects into the environment when it runs
	// our frontend container. We never call this in tests — it only makes
	// sense when invoked by BuildKit itself.
	//
	// The build function signature — func(context.Context, client.Client) —
	// is the entire contract between a BuildKit frontend and BuildKit itself.
	if err := grpcclient.RunFromEnvironment(context.Background(), build); err != nil {
		fmt.Fprintf(os.Stderr, "daeg: %v\n", err)
		os.Exit(1)
	}
}

// build is the top-level build function passed to BuildKit.
// Keeping it as a thin wrapper lets us test frontend.Build directly
// without going through the gRPC layer.
func build(ctx context.Context, c client.Client) (*client.Result, error) {
	return frontend.Build(ctx, c)
}
