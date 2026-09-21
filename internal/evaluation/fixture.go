package evaluation

import (
	"context"
	"fmt"
	"iter"

	"yonderllm/internal/provider"
)

type fixture struct{ id string }

func Fixture(id string) provider.Provider { return fixture{id: id} }
func (f fixture) Name() string            { return "fixture" }
func (f fixture) Models(context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "fixture-v1", Name: "Deterministic fixture"}}, nil
}
func (f fixture) Stream(ctx context.Context, req provider.Request) iter.Seq2[provider.Chunk, error] {
	return func(yield func(provider.Chunk, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(provider.Chunk{}, err)
			return
		}
		for _, c := range Cases() {
			if c.ID != f.id {
				continue
			}
			if c.Tool != "" && req.Messages[len(req.Messages)-1].Role != provider.RoleTool {
				args := `{"key":"color"}`
				if c.Tool == "request_write" {
					args = `{"content":"hello"}`
				}
				yield(provider.Chunk{Finish: provider.FinishTool, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: c.Tool, Arguments: args}}}, nil)
				return
			}
			yield(provider.Chunk{Delta: c.Expected, Finish: provider.FinishStop}, nil)
			return
		}
		yield(provider.Chunk{}, fmt.Errorf("unknown fixture %q", f.id))
	}
}
