package permission_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/permission"
)

func TestCheckAllowsAndDenies(t *testing.T) {
	t.Parallel()

	request := permission.Request{Tool: "builtin.read", Action: permission.ActionRead, Target: "notes/today.txt"}
	if err := permission.Check(context.Background(), permission.AllowAll{}, request); err != nil {
		t.Fatalf("AllowAll Check() error = %v", err)
	}
	for name, policy := range map[string]permission.Policy{
		"nil":      nil,
		"deny all": permission.DenyAll{},
	} {
		t.Run(name, func(t *testing.T) {
			err := permission.Check(context.Background(), policy, request)
			assertPermissionCode(t, err, permission.ErrorDenied)
			if !permission.IsDenied(fmt.Errorf("authorize: %w", err)) {
				t.Fatalf("IsDenied(%v) = false", err)
			}
		})
	}
}

func TestIsDeniedHandlesNilErrors(t *testing.T) {
	t.Parallel()

	var typedNil *permission.Error
	if permission.IsDenied(nil) || permission.IsDenied(typedNil) {
		t.Fatal("IsDenied() classified a nil error as denied")
	}
}

func TestActionPolicyUsesImmutableActionAllowlist(t *testing.T) {
	t.Parallel()

	policy, err := permission.NewActionPolicy(permission.ActionSearch, permission.ActionRead, permission.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	actions := policy.Actions()
	if len(actions) != 2 || actions[0] != permission.ActionRead || actions[1] != permission.ActionSearch {
		t.Fatalf("Actions() = %v", actions)
	}
	actions[0] = permission.ActionExecute
	if policy.Actions()[0] != permission.ActionRead {
		t.Fatal("Actions() exposed mutable policy state")
	}

	read := permission.Request{Tool: "builtin.read", Action: permission.ActionRead, Target: "file.txt"}
	if err := permission.Check(context.Background(), policy, read); err != nil {
		t.Fatalf("read Check() error = %v", err)
	}
	write := permission.Request{Tool: "builtin.write", Action: permission.ActionWrite, Target: "file.txt"}
	assertPermissionCode(t, permission.Check(context.Background(), policy, write), permission.ErrorDenied)
}

func TestActionPolicyRejectsUnknownAction(t *testing.T) {
	t.Parallel()

	if _, err := permission.NewActionPolicy(permission.Action("admin")); err == nil {
		t.Fatal("NewActionPolicy() accepted an unknown action")
	}
}

func TestCheckRejectsInvalidRequestsBeforeCallingPolicy(t *testing.T) {
	t.Parallel()

	requests := []permission.Request{
		{},
		{Tool: " read", Action: permission.ActionRead},
		{Tool: "read", Action: permission.Action("unknown")},
		{Tool: "read", Action: permission.ActionRead, Target: string([]byte{'a', 0, 'b'})},
	}
	for _, request := range requests {
		called := false
		policy := permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
			called = true
			return permission.DecisionAllow, nil
		})
		err := permission.Check(context.Background(), policy, request)
		assertPermissionCode(t, err, permission.ErrorInvalidRequest)
		if called {
			t.Fatalf("policy called for invalid request %#v", request)
		}
	}
	assertPermissionCode(t, permission.Check(nil, permission.AllowAll{}, permission.Request{
		Tool: "read", Action: permission.ActionRead,
	}), permission.ErrorInvalidRequest)
}

func TestCheckContainsPolicyFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("backend unavailable")
	request := permission.Request{Tool: "builtin.read", Action: permission.ActionRead}
	tests := []struct {
		name   string
		policy permission.Policy
		cause  error
	}{
		{
			name: "error",
			policy: permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
				return "", cause
			}),
			cause: cause,
		},
		{
			name: "invalid decision",
			policy: permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
				return permission.Decision("later"), nil
			}),
		},
		{
			name: "panic",
			policy: permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
				panic("boom")
			}),
		},
		{name: "nil function", policy: permission.PolicyFunc(nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := permission.Check(context.Background(), test.policy, request)
			assertPermissionCode(t, err, permission.ErrorPolicyFailed)
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Check() error = %v, want cause %v", err, test.cause)
			}
		})
	}
}

func TestCheckPropagatesCancellation(t *testing.T) {
	t.Parallel()

	request := permission.Request{Tool: "builtin.read", Action: permission.ActionRead}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	policy := permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
		called = true
		return permission.DecisionAllow, nil
	})
	if err := permission.Check(ctx, policy, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Check() error = %v", err)
	}
	if called {
		t.Fatal("policy called with a pre-canceled context")
	}

	ctx, cancel = context.WithCancel(context.Background())
	policy = permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
		cancel()
		return permission.DecisionAllow, nil
	})
	if err := permission.Check(ctx, policy, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled policy Check() error = %v", err)
	}
}

func TestActionPolicyIsSafeForConcurrentChecks(t *testing.T) {
	t.Parallel()

	policy, err := permission.NewActionPolicy(permission.ActionRead)
	if err != nil {
		t.Fatal(err)
	}
	request := permission.Request{Tool: "builtin.read", Action: permission.ActionRead}
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for check := 0; check < 100; check++ {
				if err := permission.Check(context.Background(), policy, request); err != nil {
					t.Errorf("Check() error = %v", err)
					return
				}
			}
		}()
	}
	group.Wait()
}

func assertPermissionCode(t *testing.T, err error, code permission.ErrorCode) {
	t.Helper()
	var permissionError *permission.Error
	if !errors.As(err, &permissionError) || permissionError.Code != code {
		t.Fatalf("error = %v, want permission code %q", err, code)
	}
}
