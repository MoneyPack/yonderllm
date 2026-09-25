// Command yonderllm is a terminal client for large language models whose
// inference always happens somewhere else.
//
// The binary is deliberately thin: everything it knows how to do lives in
// internal/cli, so the same command surface can be driven from tests without
// going through a process boundary.
package main

import (
	"os"

	"github.com/MoneyPack/yonderllm/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
