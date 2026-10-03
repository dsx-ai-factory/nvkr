// nkd-disk-inspect is the fixed entrypoint inside the inspection toolchain. All
// subject authentication, inspection and result binding lives in pkg/nkd/sbom.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if len(os.Args) != 5 {
		err := e.New(e.CodeUsage, "inspection helper requires exact tool and subject pins")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(e.ExitCode(err))
	}
	size, err := strconv.ParseInt(os.Args[4], 10, 64)
	if err != nil {
		err = e.Wrap(e.CodeUsage, err, "inspection helper subject size must be an exact integer")
	} else {
		err = sbom.RunDiskInspectionHelper(ctx, os.Args[1], os.Args[2], os.Args[3], size, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(e.ExitCode(err))
	}
}
