package main

import (
	"os"

	"github.com/mlliarm/learnx/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
