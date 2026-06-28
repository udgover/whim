package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/udgover/whim/microvm"
)

func init() {
	rootCmd.AddCommand(putCmd)
	rootCmd.AddCommand(getCmd)
}

var putCmd = &cobra.Command{
	Use:   "put <microvm-id> <local-path> <remote-dir>",
	Short: "Upload a file or directory into a running whim MicroVM",
	Long: `put uploads a local file or directory tree into an existing MicroVM,
extracting it into <remote-dir> with the basename preserved (like scp -r /
docker cp). Binary-safe.

  whim put microvm-abc123 ./src /opt`,
	Args: cobra.ExactArgs(3),
	RunE: runPut,
}

var getCmd = &cobra.Command{
	Use:   "get <microvm-id> <remote-path> <local-dir>",
	Short: "Download a file or directory out of a running whim MicroVM",
	Long: `get downloads a remote file or directory tree from an existing MicroVM
into <local-dir> with the basename preserved.

  whim get microvm-abc123 /var/log/app ./logs`,
	Args: cobra.ExactArgs(3),
	RunE: runGet,
}

func runPut(cmd *cobra.Command, args []string) error {
	id, local, remote := args[0], args[1], args[2]
	ctx, stop, sb, err := attachVM(cmd, id)
	if err != nil {
		return err
	}
	defer stop()
	printErr(cmd, "Uploading %s → %s:%s…\n", local, id, remote)
	if err := sb.Put(ctx, local, remote); err != nil {
		return whimFail(cmd, "put", err)
	}
	printErr(cmd, "Uploaded %s → %s:%s\n", local, id, remote)
	return nil
}

func runGet(cmd *cobra.Command, args []string) error {
	id, remote, local := args[0], args[1], args[2]
	ctx, stop, sb, err := attachVM(cmd, id)
	if err != nil {
		return err
	}
	defer stop()
	printErr(cmd, "Downloading %s:%s → %s…\n", id, remote, local)
	if err := sb.Get(ctx, remote, local); err != nil {
		return whimFail(cmd, "get", err)
	}
	printErr(cmd, "Downloaded %s:%s → %s\n", id, remote, local)
	return nil
}

// attachVM builds a signal-aware context and attaches to an existing MicroVM by
// id (no launch/terminate — put/get never own the VM's lifecycle). On error it
// returns a whim-level (125) failure and has already released the signal handler;
// on success the caller must defer the returned stop.
func attachVM(cmd *cobra.Command, id string) (context.Context, context.CancelFunc, *microvm.Sandbox, error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	cfg, err := buildAWSConfig(ctx, cmd)
	if err != nil {
		stop()
		return nil, nil, nil, whimFail(cmd, "load AWS config", err)
	}
	sb, err := microvm.NewFromConfig(cfg).Attach(ctx, id)
	if err != nil {
		stop()
		return nil, nil, nil, whimFail(cmd, "attach", err)
	}
	return ctx, stop, sb, nil
}
