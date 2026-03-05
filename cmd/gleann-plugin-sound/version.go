package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of gleann-plugin-sound",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("gleann-sound %s\n", version)
		},
	}
}
