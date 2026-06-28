package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "whim",
	Short: "Ephemeral AWS Lambda MicroVM shells",
	Long: `whim spawns throwaway root shells in AWS Lambda MicroVMs.

Run 'whim init' once to build the default sandbox image, then 'whim' to
drop into an interactive shell or 'whim run -- <cmd>' to stream output.`,
}

func init() {
	rootCmd.PersistentFlags().String("region", "", "AWS region (overrides the credential chain's default)")
	rootCmd.PersistentFlags().String("profile", "", "AWS shared-config profile")
	// Runtime errors shouldn't dump command usage — only genuine usage errors should.
	rootCmd.SilenceUsage = true
	// We render errors ourselves in Execute (so exitCodeError carries a code and
	// isn't printed as "Error: exit status N").
	rootCmd.SilenceErrors = true
}

// exitCodeError carries a specific process exit code up to Execute. run/exec use
// it to propagate a remote command's exit status (and whim-level failures as
// whimExitCode) instead of the generic exit 1.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func Execute() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	var ece exitCodeError
	if errors.As(err, &ece) {
		os.Exit(ece.code) // any message was already printed by the command
	}
	fmt.Fprintln(os.Stderr, "Error:", redactAccountID(err.Error()))
	os.Exit(1)
}

// printOut writes a timestamped status line to cmd's stdout. The caller's
// format should terminate with a newline. Write errors are intentionally
// ignored for CLI progress messages.
func printOut(cmd *cobra.Command, format string, args ...any) {
	msg := redactAccountID(fmt.Sprintf(format, args...))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s", time.Now().Format("15:04:05"), msg)
}

// printErr writes a timestamped error line to cmd's stderr.
func printErr(cmd *cobra.Command, format string, args ...any) {
	msg := redactAccountID(fmt.Sprintf(format, args...))
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[%s] %s", time.Now().Format("15:04:05"), msg)
}
