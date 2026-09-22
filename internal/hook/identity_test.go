package hook_test

import (
	"testing"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/maadiii/gogate/internal/hook"
)

func TestIdentity_RoundTripIsDetached(t *testing.T) {
	t.Parallel()

	rc := ut.CreateUtRequestContext("GET", "/ping", &ut.Body{})
	provided := hook.Identity{
		Subject:     "user-1",
		Roles:       []string{"member"},
		Permissions: []string{"reports:read"},
		AuthMethod:  "paseto",
	}

	hook.SetIdentity(rc, provided)
	provided.Roles[0] = "admin"
	provided.Permissions[0] = "reports:write"

	got, ok := hook.IdentityFrom(rc)
	if !ok {
		t.Fatal("expected identity to be present")
	}
	if got.Subject != "user-1" || got.Roles[0] != "member" || got.Permissions[0] != "reports:read" || got.AuthMethod != "paseto" {
		t.Fatalf("identity = %+v, want the original values", got)
	}

	got.Roles[0] = "operator"
	got.Permissions[0] = "reports:delete"
	again, ok := hook.IdentityFrom(rc)
	if !ok || again.Roles[0] != "member" || again.Permissions[0] != "reports:read" {
		t.Fatalf("identity was mutated through a read: %+v", again)
	}
}

func TestIdentityFrom_Missing(t *testing.T) {
	t.Parallel()

	rc := ut.CreateUtRequestContext("GET", "/ping", &ut.Body{})
	if identity, ok := hook.IdentityFrom(rc); ok || identity.Subject != "" || identity.AuthMethod != "" || len(identity.Roles) != 0 || len(identity.Permissions) != 0 {
		t.Fatalf("IdentityFrom() = (%+v, %v), want no identity", identity, ok)
	}
}
