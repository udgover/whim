package main

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridable at build time: -ldflags "-X main.version=v0.1.0".
// Otherwise it is resolved from the module/VCS build info.
var version = ""

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the whim version and build revision",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := cmd.OutOrStdout().Write([]byte(versionString() + "\n"))
		return err
	},
}

// versionString resolves the version from the -ldflags override, else the module
// version (set when `go install`ed at a tag), else "(devel)", and appends the
// short VCS revision when the binary was built from a checkout.
func versionString() string {
	v := version
	rev, dirty := "", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "" && bi.Main.Version != "" {
			v = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if v == "" {
		v = "(devel)"
	}
	out := "whim " + v
	if len(rev) >= 7 {
		out += " (" + rev[:7]
		if dirty {
			out += ", modified"
		}
		out += ")"
	}
	return out
}
