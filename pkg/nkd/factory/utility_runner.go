package factory

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// utilityRunnerLocation describes only the two maintained helper transports.
// Runtime, image, network and platform come from the admitted execution owner;
// these private paths are not another authored execution contract.
type utilityRunnerLocation struct {
	runtime                  string
	mountSource, mountTarget string
	helper, plan, planDigest string
	user                     string
	home                     bool
}

// executeUtilityNode is shared by local orchestration and hosted helper steps.
// The caller validates its complete Plan before invoking this private launcher.
func executeUtilityNode(ctx context.Context, node Node, location utilityRunnerLocation, stdout, stderr io.Writer) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "utility execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "utility execution canceled")
	}
	network := "bridge"
	if node.Network == ir.NetworkNone {
		network = "none"
	}
	name := "nkd-factory-" + sanitizeLocalNodeID(node.ID) + "-" + strconv.Itoa(os.Getpid())
	args := []string{"run", "--rm", "--name", name, "--network", network, "--platform", node.Platform}
	if location.home {
		args = append(args, "-e", "HOME=/tmp")
	}
	if location.user != "" {
		args = append(args, "--user", location.user)
	}
	args = append(args, "-v", location.mountSource+":"+location.mountTarget, "-w", "/workspace",
		node.Container, location.helper, "factory", "execute-node", "--plan", location.plan, "--node", node.ID, "--workspace", ".nkd/factory")
	if location.planDigest != "" {
		args = append(args, "--plan-digest", location.planDigest)
	}
	cmd := exec.CommandContext(ctx, location.runtime, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := ownedcmd.Run(ctx, cmd)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = ownedcmd.CombinedOutput(cleanupCtx, location.runtime, "rm", "-f", name)
	cancel()
	return classifyContext(ctx, runErr)
}
