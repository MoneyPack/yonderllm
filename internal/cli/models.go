package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/terminaltext"
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
		Long: "List the models available from a provider, cheapest tier first.\n\n" +
			"Free and cheap models are shown by default. Cheap means both the input\n" +
			"and the output rate sit at or under $1 per million tokens, which covers\n" +
			"the small remote models yonderllm is built around; a frontier model\n" +
			"listed beside them is an easy way to spend money by accident, so those\n" +
			"are hidden until you pass --all. Models a provider quotes no price for\n" +
			"are shown as unknown rather than guessed at.\n\n" +
			"The PRICE/1M column reads input rate then output rate, in US dollars\n" +
			"per million tokens.\n\n" +
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

	cmd.Flags().BoolVar(&all, "all", false, "include models priced above the cheap tier")
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
		if m.Affordable() {
			kept = append(kept, m)
		}
	}
	return kept
}

// sortModels orders models cheapest tier first and then by id.
//
// Ordering by id rather than by the provider's own order makes the output
// diffable between runs, and leading with the cheapest tier means the models
// yonderllm is meant to be pointed at stay visible without scrolling under
// --all. Within a tier the rates are not compared, because a catalogue is read
// to pick a model rather than to shave hundredths of a cent off one.
func sortModels(models []provider.Model) {
	sort.SliceStable(models, func(i, j int) bool {
		li, lj := models[i].Tier().Rank(), models[j].Tier().Rank()
		if li != lj {
			return li < lj
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
			// Distinguishing "nothing at all" from "nothing cheap
			// enough" saves the user from concluding the provider is
			// broken.
			fmt.Fprintf(out, "%s reports no free or cheap models. Try --all.\n", providerName)
		}
		return nil
	}

	// A tabwriter is flushed once at the end, so a write error surfaces
	// there rather than on every column.
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  MODEL\tNAME\tCONTEXT\tPRICE/1M\tTIER")
	for _, m := range models {
		marker := " "
		if m.ID == active {
			marker = "*"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\n",
			marker, terminaltext.Line(m.ID), terminaltext.Line(displayName(m)), contextLabel(m.ContextWindow),
			priceLabel(m.Pricing), tierLabel(m.Tier()))
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
	Tier          string `json:"tier"`
	// Pricing is omitted rather than zeroed when a provider quotes no
	// price, so a consumer cannot mistake silence for free.
	Pricing *wirePricing `json:"pricing,omitempty"`
	Active  bool         `json:"active"`
}

// wirePricing is the JSON shape of a model's rates.
//
// The figures are per million tokens rather than the per-token rates the
// adapters decode, because that is the unit prices are quoted and compared in
// everywhere outside an API response, and a consumer reading this document is
// far likelier to want the number it can show a human than the raw one.
type wirePricing struct {
	PromptUSDPerMillion     float64 `json:"prompt_usd_per_million"`
	CompletionUSDPerMillion float64 `json:"completion_usd_per_million"`
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
		entry := wireModel{
			ID:            m.ID,
			Name:          displayName(m),
			ContextWindow: m.ContextWindow,
			Tier:          string(m.Tier()),
			Active:        m.ID == active,
		}
		// An unpriced model carries no pricing member at all, so a
		// consumer has to handle the absence deliberately instead of
		// reading a zero rate as free.
		if m.Pricing.Known {
			entry.Pricing = &wirePricing{
				PromptUSDPerMillion:     perMillion(m.Pricing.Prompt),
				CompletionUSDPerMillion: perMillion(m.Pricing.Completion),
			}
		}
		doc.Models = append(doc.Models, entry)
	}

	enc := json.NewEncoder(terminaltext.Raw(cmd.OutOrStdout()))
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

// perMillion converts a per-token rate into the per-million-token figure
// prices are quoted in outside an API response.
func perMillion(rate float64) float64 {
	return rate * 1e6
}

// priceLabel renders a model's rates as input then output, in US dollars per
// million tokens.
//
// Both rates are shown because they diverge by an order of magnitude on the
// small remote models yonderllm targets, and a single blended number would hide
// which half of a conversation is the expensive one. A provider that quotes no
// price reads as unknown rather than as free, so the pricing column never
// invents a figure the provider did not publish.
func priceLabel(p provider.Pricing) string {
	if !p.Known {
		return "unknown"
	}
	return fmt.Sprintf("%s / %s", rateLabel(p.Prompt), rateLabel(p.Completion))
}

// rateLabel formats one per-token rate as a per-million-token figure.
//
// Two decimals is the resolution that separates the cheap models from each
// other; a genuinely free model prints as a plain 0 rather than 0.00 so it
// stands out in a column of priced neighbours.
func rateLabel(rate float64) string {
	perM := perMillion(rate)
	if perM == 0 {
		return "0"
	}
	return strconv.FormatFloat(perM, 'f', 2, 64)
}

// tierLabel names the billing tier of a model.
//
// The [provider.Tier] values are already the words meant for a reader, so this
// is a deliberate narrowing rather than a translation: it keeps the table from
// printing whatever a future tier constant happens to be spelled as.
func tierLabel(t provider.Tier) string {
	switch t {
	case provider.TierFree, provider.TierCheap, provider.TierPaid:
		return string(t)
	default:
		return string(provider.TierUnknown)
	}
}
