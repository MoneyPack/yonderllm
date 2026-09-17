package cli

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"runtime"
	"runtime/debug"
)

// BuildCommit and BuildDate may be supplied with Go linker -X flags.
var BuildCommit, BuildDate string

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "version", Short: "Show version and build metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
		}{Version, commit, date, modified, runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH, 1}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "yonderllm %s\ncommit %s\nbuilt %s\n%s %s\nNDJSON schema 1\n", Version, orDash(commit), orDash(date), info.GoVersion, info.Platform)
		return err
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit build metadata as JSON")
	return cmd
}
