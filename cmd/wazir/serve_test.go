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
	if v := cmd.Flags().Lookup("addr").DefValue; v != ":8080" {
		t.Errorf("addr default = %q, want :8080", v)
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
