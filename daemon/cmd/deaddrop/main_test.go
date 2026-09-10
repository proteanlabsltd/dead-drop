package main

import (
	"context"
	"strings"
	"testing"
)

func TestCommandValidation(t *testing.T) {
	cases := [][]string{{}, {"unknown"}, {"get", "host:/file"}, {"put", "--overwrite"}, {"ls", "--overwrite", "host:/"}, {"mkdir", "bad"}}
	for _, args := range cases {
		if e := run(context.Background(), args); e == nil {
			t.Errorf("accepted %q", args)
		}
	}
}
func TestRemoteFlagsAfterOperands(t *testing.T) {
	e := remoteCommand(context.Background(), []string{"put", "missing-local", "host:/remote", "--overwrite"})
	if e == nil || !strings.Contains(e.Error(), "missing-local") {
		t.Fatalf("unexpected error %v", e)
	}
}
