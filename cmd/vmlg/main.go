// Command vmlg queries a vmlookingglass server for scrape targets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/evkuzin/vmLookingGlass/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdout); err != nil {
		// A usage error has already been reported by the flag package.
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "vmlg:", err)
		}
		os.Exit(1)
	}
}
