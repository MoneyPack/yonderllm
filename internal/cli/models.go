package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"yonderllm/internal/provider"
)

// newModelsCmd builds the model catalogue command.
//
// Listing models needs a provider but not a conversation, so this command
// reaches for the resolver directly rather than going through
// [env.newSession]. That keeps a read-only query from touching the daily
// request budget or allocating conversation history.
func newModelsCmd(e *env) *cobra.Command {
	var (
		all    bool
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "models [provider]",
		Short: "List the models a provider offers",
		Long: "List the models available from a provider, newest catalogue first.\n\n" +
			"Only free-tier models are shown by default, because yonderllm is built\n" +
			"to run on free tiers and a paid model listed alongside them is an easy\n" +
			"way to spend money by accident. Pass --all to see everything.\n\n" +
			"With no argument the active provider is used, so --provider and the\n" +
			"positional form are interchangeable.",
		Example: `yonderllm models
yonderllm models openrouter --all
yonderllm models --json | jq -r '.models[].id'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := e.resolve()
			if err != nil {
				return err
			}

			// A positional provider is a convenience spelling of the
			// global flag, and it wins for this one invocation only.
			name := cfg.Provider
			if len(args) == 1 {
				name = args[0]
			}

			p, err := newResolver(cfg)(name)
			if err != nil {
				return err
			}

			ctx, stop := signalContext(cmd)
			defer stop()

			models, err := p.Models(ctx)
			if err != nil {
				return fmt.Errorf("listing models for %s: %w", name, err)
			}

			models = filterModels(models, all)
			sortModels(models)

			// The configured model is highlighted so that a user can see
			// at a glance whether their config still names something the
			// provider actually offers.
			active := cfg.Providers[name].Model

			if asJSON {
				return writeModelsJSON(cmd, name, active, models)
			}
			return writeModelsTable(cmd, name, active, models, all)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "include models that are not on the free tier")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the catalogue as a JSON object")
	return cmd
}

// filterModels drops paid models unless the caller asked for everything.
func filterModels(models []provider.Model, all bool) []provider.Model {
	if all {
		return models
	}
	kept := make([]provider.Model, 0, len(models))
	for _, m := range models {
		if m.Free {
			kept = append(kept, m)
		}
	}
	return kept
}

// sortModels orders free models first and then by id.
//
// Ordering by id rather than by the provider's own order makes the output
// diffable between runs, and putting free models on top means the ones a free
// tier can actually reach are visible without scrolling under --all.
func sortModels(models []provider.Model) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Free != models[j].Free {
			return models[i].Free
		}
		return models[i].ID < models[j].ID
	})
}

// writeModelsTable renders the catalogue for a human reader.
func writeModelsTable(cmd *cobra.Command, providerName, active string, models []provider.Model, all bool) error {
	out := cmd.OutOrStdout()

	if len(models) == 0 {
		if all {
			fmt.Fprintf(out, "%s reports no models.\n", providerName)
		} else {
			// Distinguishing "nothing at all" from "nothing free" saves
			// the user from concluding the provider is broken.
			fmt.Fprintf(out, "%s reports no free-tier models. Try --all.\n", providerName)
		}
		return nil
	}

	// A tabwriter is flushed once at the end, so a write error surfaces
	// there rather than on every column.
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  MODEL\tNAME\tCONTEXT\tTIER")
	for _, m := range models {
		marker := " "
		if m.ID == active {
			marker = "*"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\n",
			marker, m.ID, displayName(m), contextLabel(m.ContextWindow), tierLabel(m.Free))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if active != "" {
		fmt.Fprintf(out, "\n* active model for %s\n", providerName)
	}
	return nil
}

// wireModel is the JSON shape of one catalogue entry.
//
// Like the run command's event types, it is declared separately from
// [provider.Model] so that the published contract does not shift the next time
// an internal field is renamed.
type wireModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
	Free          bool   `json:"free"`
	Active        bool   `json:"active"`
}

// wireCatalogue is the top-level JSON document written by models --json.
type wireCatalogue struct {
	Provider string      `json:"provider"`
	Active   string      `json:"active_model,omitempty"`
	Models   []wireModel `json:"models"`
}

// writeModelsJSON renders the catalogue as a single indented object.
//
// This is one document rather than the newline-delimited stream that run --json
// produces, because a catalogue is a finite answer to a finite question; there
// is nothing to consume incrementally.
func writeModelsJSON(cmd *cobra.Command, providerName, active string, models []provider.Model) error {
	doc := wireCatalogue{
		Provider: providerName,
		Active:   active,
		// A non-nil slice keeps an empty catalogue as [] rather than
		// null, so consumers can range over it unconditionally.
		Models: make([]wireModel, 0, len(models)),
	}
	for _, m := range models {
		doc.Models = append(doc.Models, wireModel{
			ID:            m.ID,
			Name:          displayName(m),
			ContextWindow: m.ContextWindow,
			Free:          m.Free,
			Active:        m.ID == active,
		})
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// displayName falls back to the id when a provider supplies no label, so the
// column is never blank.
func displayName(m provider.Model) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// contextLabel renders a context window, spelling out the unknown case rather
// than printing a bare 0 that reads like a real limit.
func contextLabel(tokens int) string {
	if tokens <= 0 {
		return "unknown"
	}
	if tokens%1024 == 0 {
		return strconv.Itoa(tokens/1024) + "K"
	}
	return strconv.Itoa(tokens)
}

// tierLabel names the billing tier of a model.
func tierLabel(free bool) string {
	if free {
		return "free"
	}
	return "paid"
}
