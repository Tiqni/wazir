package main

import "testing"

func TestServeCmdHasAddrFlag(t *testing.T) {
	cmd := newServeCmd()
	if cmd.Use != "serve" {
		t.Errorf("Use = %q, want serve", cmd.Use)
	}
	if cmd.Flags().Lookup("addr") == nil {
		t.Error("serve must define an --addr flag")
	}
}

func TestRootRegistersServe(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "serve" {
			return
		}
	}
	t.Error("root command tree is missing 'serve'")
}
