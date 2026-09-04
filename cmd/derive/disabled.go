//go:build !awsderive

// This file exists so `go build ./...` succeeds without the awsderive tag. A
// package whose only file is tag-excluded fails with "build constraints exclude
// all Go files", which would break the ordinary build for everyone.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"derive is built out of this binary.\n\n"+
			"It creates real, billable AWS resources, so it is behind a build tag:\n"+
			"  go run -tags awsderive ./cmd/derive --type aws_vpc --fixture ./derivefixtures/aws_vpc\n\n"+
			"It also requires PREFLIGHT_DERIVE_ACCOUNT to match the live caller's\n"+
			"account, and PREFLIGHT_DERIVE_CONFIRM=creates-real-resources.")
	os.Exit(2)
}
