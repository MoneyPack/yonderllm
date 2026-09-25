package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/MoneyPack/yonderllm/internal/session"
)

// openSessions returns the store rooted beside user configuration, creating it if
// needed. All the session subcommands and the TUI's /save share this path.
func openSessions() (*session.Sessions, error) {
	return session.DefaultSessions()
}

// newSessionsCmd builds the conversation-store command.
//
// The TUI's /save calls into the same store on the same path, so a conversation
// saved in the TUI can be listed or deleted here, and vice versa.
func newSessionsCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List and manage saved conversations",
		Long: "List saved conversations and delete ones you no longer want.\n\n" +
			"Conversations are saved with /save inside the interactive session and\n" +
			"resumed with --resume or --last. Recognized secrets are redacted\n" +
			"before saving; saved files are not encrypted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSessions()
			if err != nil {
				return err
			}
			list, err := s.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no saved conversations")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tMESSAGES\tPROVIDER\tMODEL")
			for _, c := range list {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", c.Name, len(c.Messages), c.Provider, c.Model)
			}
			return w.Flush()
		},
	}

	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved conversation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openSessions()
			if err != nil {
				return err
			}
			return s.Delete(args[0])
		},
	}
	cmd.AddCommand(deleteCmd)
	return cmd
}

// resolveSessionOrDefault returns a fresh session, optionally resuming a named
// conversation. When resumeName is empty it is a normal empty session.
func (e *env) resolveSessionOrDefault() (*session.Session, error) {
	sess, err := e.newSession()
	if err != nil {
		return nil, err
	}
	if e.resume == "" && !e.last && e.save == "" {
		return sess, nil
	}
	_, err = e.prepareConversation(sess, false)
	return sess, err
}

func (e *env) prepareConversation(sess *session.Session, interactive bool) (*session.Sessions, error) {
	s, err := openSessions()
	if err != nil {
		return nil, err
	}
	if e.resume != "" || e.last {
		var conv session.SavedConversation
		if e.last {
			conv, err = s.Latest()
		} else {
			conv, err = s.Get(e.resume)
		}
		if err != nil {
			return nil, fmt.Errorf("resume: %w", err)
		}
		if e.provider != "" {
			conv.Provider = e.provider
			conv.Model = sess.Model()
		}
		if e.model != "" {
			conv.Model = e.model
		}
		if err = sess.LoadConversation(conv); err != nil {
			return nil, err
		}
	}
	if !e.noSave && (interactive || e.save != "" || e.resume != "" || e.last) {
		if err = sess.EnableSaving(s, e.save); err != nil {
			return nil, err
		}
	}
	return s, nil
}
