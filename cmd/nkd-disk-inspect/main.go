// nkd-disk-inspect is the fixed entrypoint inside the inspection toolchain. All
// subject authentication, inspection and result binding lives in pkg/nkd/sbom.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	status := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if status != 0 {
		os.Exit(status)
	}
}

// run maps flags and the owning SBOM operation to the frozen CLI exit table.
func run(ctx context.Context, args []string, input io.Reader, output, diagnostic io.Writer) int {
	if len(args) != 4 {
		err := e.New(e.CodeUsage, "inspection helper requires exact tool and subject pins")
		fmt.Fprintln(diagnostic, err)
		return e.ExitCode(err)
	}
	size, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		err = e.Wrap(e.CodeUsage, err, "inspection helper subject size must be an exact integer")
	} else {
		err = sbom.RunDiskInspectionHelper(ctx, args[0], args[1], args[2], size, input, output)
	}
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return e.ExitCode(err)
	}
	return 0
}
