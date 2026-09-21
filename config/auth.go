package config

import "fmt"

// Auth holds the token settings shared by every route that authenticates with
// JWTs.
//
// The block as a whole is optional: a gateway whose routes are all public does
// not need it, and an omitted block decodes to the zero value. But anything the
// block does configure has to be complete — a half-written token entry is an
// error, not a default, because the alternative is a token that silently never
// expires.
type Auth struct {
	AccessToken  AccessToken  `yaml:"accessToken"`
	RefreshToken RefreshToken `yaml:"refreshToken"`
}

type AccessToken struct {
	Secret string   `yaml:"secret"`
	TTL    Duration `yaml:"ttl"`
}

type RefreshToken struct {
	Secret string   `yaml:"secret"`
	TTL    Duration `yaml:"ttl"`
}

// set reports whether any field of the token block was supplied. This is what
// tells an omitted block (zero value, valid) apart from a half-written one.
func (a AccessToken) set() bool {
	return a.Secret != "" || a.TTL != 0
}

func (r RefreshToken) set() bool {
	return r.Secret != "" || r.TTL != 0
}

func (a Auth) validate() error {
	if !a.AccessToken.set() && !a.RefreshToken.set() {
		return nil
	}

	// The access token is the point of the block; a refresh token is optional,
	// but you cannot have one without the other.
	if !a.AccessToken.set() {
		return fmt.Errorf("auth.accessToken: must be configured when auth is set")
	}

	if err := validateToken("auth.accessToken", a.AccessToken.Secret, a.AccessToken.TTL); err != nil {
		return err
	}

	if !a.RefreshToken.set() {
		return nil
	}

	return validateToken("auth.refreshToken", a.RefreshToken.Secret, a.RefreshToken.TTL)
}

// validateToken checks one token block. field is the YAML path of the block, so
// the error names exactly which setting is wrong.
func validateToken(field, secret string, ttl Duration) error {
	if secret == "" {
		return fmt.Errorf("%s.secret: cannot be empty", field)
	}

	if ttl <= 0 {
		return fmt.Errorf("%s.ttl: must be greater than zero", field)
	}

	return nil
}
