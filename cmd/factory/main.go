// Command factory is the AVM Intelligence orchestrator. This initial command
// exposes only the pinned consumer contract. It has no GitHub, publication,
// or model credentials.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/softwareone-public/avm-intelligence/internal/toolchain"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("factory", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	toolchainPath := flags.String("toolchain", "config/toolchain.yaml", "Pinned IaCLens toolchain document")
	show := flags.Bool("show-toolchain", false, "Validate and print the pinned IaCLens consumer contract")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*show {
		return fmt.Errorf("no factory operation requested; analysis and publication are not enabled in this skeleton")
	}
	doc, err := toolchain.Load(*toolchainPath)
	if err != nil {
		return err
	}
	asset, err := doc.Asset("linux", "amd64")
	if err != nil {
		return err
	}
	fmt.Printf("iaclens %s (%s), schema %d, adapter %d, linux/amd64 %s\n", doc.CLI.Version, doc.CLI.Commit, doc.CLI.SchemaVersion, doc.Adapter.Version, asset.SHA256)
	return nil
}
