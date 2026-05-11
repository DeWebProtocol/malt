package main

import "github.com/dewebprotocol/malt/core/cas/ipfs"

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
