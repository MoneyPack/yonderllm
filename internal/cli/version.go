package cli

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/MoneyPack/yonderllm/internal/terminaltext"
)

// BuildCommit and BuildDate may be supplied with Go linker -X flags.
var BuildCommit, BuildDate string

// resolvedVersion is Version unless nothing stamped it, in which case the
// module version Go recorded at build time is used. That is what a
// `go install github.com/MoneyPack/yonderllm/cmd/yonderllm@v0.2.0` binary
// carries: the release workflow stamps Version, but `go install` cannot, and
// "dev" would be a lie about a binary built from a tagged release. A checkout
// build reports "(devel)" there, which is left as "dev".
func resolvedVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "version", Short: "Show version and build metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		version := resolvedVersion()
		commit, date := BuildCommit, BuildDate
		modified := false
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					if commit == "" {
						commit = s.Value
					}
				case "vcs.time":
					if date == "" {
						date = s.Value
					}
				case "vcs.modified":
					modified = s.Value == "true"
				}
			}
		}
		info := struct {
			Version       string `json:"version"`
			Commit        string `json:"commit,omitempty"`
			Date          string `json:"build_date,omitempty"`
			Modified      bool   `json:"modified"`
			GoVersion     string `json:"go_version"`
			Platform      string `json:"platform"`
			SchemaVersion int    `json:"schema_version"`
		}{version, commit, date, modified, runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH, ndjsonSchemaVersion}
		if asJSON {
			return json.NewEncoder(terminaltext.Raw(cmd.OutOrStdout())).Encode(info)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "yonderllm %s\ncommit %s\nbuilt %s\n%s %s\nNDJSON schema %d\n", version, orDash(commit), orDash(date), info.GoVersion, info.Platform, ndjsonSchemaVersion)
		return err
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit build metadata as JSON")
	return cmd
}
