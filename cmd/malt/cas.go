package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/cas/ipfs"
	"github.com/spf13/cobra"
)

var casCmd = &cobra.Command{
	Use:   "cas",
	Short: "Interact directly with the configured CAS endpoint",
}

var casPutCmd = &cobra.Command{
	Use:   "put <file>",
	Short: "Put a file into CAS",
	Args:  cobra.ExactArgs(1),
	RunE:  runCASPut,
}

var casGetOut string

var casGetCmd = &cobra.Command{
	Use:   "get <cid>",
	Short: "Get raw block content from CAS",
	Args:  cobra.ExactArgs(1),
	RunE:  runCASGet,
}

var casHasCmd = &cobra.Command{
	Use:   "has <cid>",
	Short: "Check whether a block exists in CAS",
	Args:  cobra.ExactArgs(1),
	RunE:  runCASHas,
}

func init() {
	rootCmd.AddCommand(casCmd)
	casCmd.AddCommand(casPutCmd)
	casCmd.AddCommand(casGetCmd)
	casCmd.AddCommand(casHasCmd)
	casGetCmd.Flags().StringVar(&casGetOut, "out", "", "write block content to a file instead of stdout")
}

func runCASPut(cmd *cobra.Command, args []string) error {
	client, err := makeCASClient()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	blockCID, err := client.Put(cmd.Context(), data)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, blockCID.String())
	return nil
}

func runCASGet(cmd *cobra.Command, args []string) error {
	client, err := makeCASClient()
	if err != nil {
		return err
	}
	blockCID, err := parseCID(args[0])
	if err != nil {
		return err
	}
	data, err := client.Get(cmd.Context(), blockCID)
	if err != nil {
		return err
	}

	if casGetOut != "" {
		return os.WriteFile(casGetOut, data, 0o644)
	}
	_, err = os.Stdout.Write(data)
	return err
}

func runCASHas(cmd *cobra.Command, args []string) error {
	client, err := makeCASClient()
	if err != nil {
		return err
	}
	blockCID, err := parseCID(args[0])
	if err != nil {
		return err
	}
	ok, err := client.Has(cmd.Context(), blockCID)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, ok)
	return nil
}

func makeCASClient() (*ipfs.Client, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	timeout, err := cfg.CASTimeout()
	if err != nil {
		return nil, err
	}
	return ipfs.NewClient(cfg.CASBaseURL(), ipfs.WithTimeout(timeout)), nil
}
