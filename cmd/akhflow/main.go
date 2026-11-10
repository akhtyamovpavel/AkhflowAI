package main

import (
	"fmt"
	"os"

	"akhflow.ai/akhflow/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
