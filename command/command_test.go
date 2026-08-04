package command_test

import (
	"context"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/command"
)

func TestRequestValidate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		request command.Request
		valid   bool
	}{
		{name: "root", request: command.Request{Script: "pwd", Directory: "."}, valid: true},
		{name: "nested", request: command.Request{Script: "pwd", Directory: "work/tree"}, valid: true},
		{name: "missing script", request: command.Request{Directory: "."}},
		{name: "script null", request: command.Request{Script: "a\x00b", Directory: "."}},
		{name: "missing directory", request: command.Request{Script: "pwd"}},
		{name: "absolute directory", request: command.Request{Script: "pwd", Directory: "/tmp"}},
		{name: "parent directory", request: command.Request{Script: "pwd", Directory: "../tmp"}},
		{name: "unclean directory", request: command.Request{Script: "pwd", Directory: "work/../tmp"}},
		{name: "backslash directory", request: command.Request{Script: "pwd", Directory: `work\tmp`}},
		{name: "directory null", request: command.Request{Script: "pwd", Directory: "a\x00b"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() accepted an invalid request")
			}
		})
	}
}

func TestResultCloneAndValidate(t *testing.T) {
	t.Parallel()

	original := command.Result{Stdout: []byte("out"), Stderr: []byte("err"), ExitCode: -1}
	clone := original.Clone()
	clone.Stdout[0] = 'X'
	clone.Stderr[0] = 'Y'
	if string(original.Stdout) != "out" || string(original.Stderr) != "err" {
		t.Fatalf("Clone() shares output buffers: %#v", original)
	}
	if err := original.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := (command.Result{ExitCode: -2}).Validate(); err == nil {
		t.Fatal("Validate() accepted an exit code below -1")
	}
}

func TestNilRunnerFuncReturnsError(t *testing.T) {
	t.Parallel()

	var runner command.RunnerFunc
	if _, err := runner.Run(context.Background(), command.Request{Script: "true", Directory: "."}); err == nil {
		t.Fatal("RunnerFunc(nil) returned no error")
	}
}
