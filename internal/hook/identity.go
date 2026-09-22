package hook

import "github.com/cloudwego/hertz/pkg/app"

// Identity is the authenticated caller information available to hooks for one
// request. Authentication hooks produce it; hooks such as quota and
// entitlement consume it.
type Identity struct {
	Subject     string
	Roles       []string
	Permissions []string
	AuthMethod  string
}

// identityKey is deliberately private. Code outside this package must use the
// typed accessors instead of sharing an undocumented rc.Set key.
const identityKey = "github.com/maadiii/gogate/hook.identity"

// SetIdentity stores identity for the lifetime of rc. Slice fields are copied
// so a caller cannot mutate the shared request value after setting it.
func SetIdentity(rc *app.RequestContext, identity Identity) {
	rc.Set(identityKey, cloneIdentity(identity))
}

// IdentityFrom returns the authenticated caller for rc. The returned slices
// are copies, so consumers cannot mutate data another hook relies on.
func IdentityFrom(rc *app.RequestContext) (Identity, bool) {
	value, ok := rc.Get(identityKey)
	if !ok {
		return Identity{}, false
	}

	identity, ok := value.(Identity)
	if !ok {
		return Identity{}, false
	}

	return cloneIdentity(identity), true
}

func cloneIdentity(identity Identity) Identity {
	identity.Roles = append([]string(nil), identity.Roles...)
	identity.Permissions = append([]string(nil), identity.Permissions...)

	return identity
}
