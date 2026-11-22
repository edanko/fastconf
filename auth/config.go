package auth

import (
	"fmt"
	"strings"
)

// Authenticator type names accepted by Config.Type. The empty string and
// "none" select an authenticator that leaves requests unchanged.
const (
	TypeBearer  = "bearer"
	TypeBasic   = "basic"
	TypeHeader  = "header"
	TypeHeaders = "headers"
	TypeHMAC    = "hmac"
	TypeOAuth2  = "oauth2"
	TypeNone    = "none"
)

// Config is a static authenticator description that can be decoded from a
// configuration document.
//
// Only the fields relevant to Type are used:
//
//	TypeBearer:  Token
//	TypeBasic:   Username, Password
//	TypeHeader:  Header (name), Value
//	TypeHeaders: Headers
//	TypeHMAC:    Secret, SignatureHeader, TimestampHeader
//	TypeOAuth2:  OAuth2
//
// The struct carries mapstructure, JSON, YAML and TOML tags so any supported
// decoder can fill it.
type Config struct {
	Type     string            `mapstructure:"type" json:"type" yaml:"type" toml:"type"`
	Token    string            `mapstructure:"token" json:"token" yaml:"token" toml:"token"`
	Username string            `mapstructure:"username" json:"username" yaml:"username" toml:"username"`
	Password string            `mapstructure:"password" json:"password" yaml:"password" toml:"password"`
	Header   string            `mapstructure:"header" json:"header" yaml:"header" toml:"header"`
	Value    string            `mapstructure:"value" json:"value" yaml:"value" toml:"value"`
	Headers  map[string]string `mapstructure:"headers" json:"headers" yaml:"headers" toml:"headers"`

	Secret          string `mapstructure:"secret" json:"secret" yaml:"secret" toml:"secret"`
	SignatureHeader string `mapstructure:"signatureHeader" json:"signatureHeader" yaml:"signatureHeader" toml:"signatureHeader"`
	TimestampHeader string `mapstructure:"timestampHeader" json:"timestampHeader" yaml:"timestampHeader" toml:"timestampHeader"`

	OAuth2 OAuth2Config `mapstructure:"oauth2" json:"oauth2" yaml:"oauth2" toml:"oauth2"`
}

// FromConfig builds an authenticator from a static configuration. Unknown
// types and missing required fields are reported as errors; an empty Type
// returns a no-op authenticator so callers never have to treat nil as "no
// authentication".
func FromConfig(config Config) (Authenticator, error) {
	switch strings.ToLower(strings.TrimSpace(config.Type)) {
	case "", TypeNone:
		return Noop(), nil
	case TypeBearer:
		if config.Token == "" {
			return nil, fmt.Errorf("auth: bearer: token is required")
		}
		return Bearer(config.Token), nil
	case TypeBasic:
		if config.Username == "" {
			return nil, fmt.Errorf("auth: basic: username is required")
		}
		return Basic(config.Username, config.Password), nil
	case TypeHeader:
		if config.Header == "" {
			return nil, fmt.Errorf("auth: header: header name is required")
		}
		return Header(config.Header, config.Value), nil
	case TypeHeaders:
		if len(config.Headers) == 0 {
			return nil, fmt.Errorf("auth: headers: no headers configured")
		}
		for name := range config.Headers {
			if name == "" {
				return nil, fmt.Errorf("auth: headers: empty header name")
			}
		}
		return Headers(config.Headers), nil
	case TypeHMAC:
		if len(config.Secret) == 0 {
			return nil, fmt.Errorf("auth: hmac: secret is required")
		}
		options := make([]HMACOption, 0, 2)
		if config.SignatureHeader != "" {
			options = append(options, WithHMACHeader(config.SignatureHeader))
		}
		if config.TimestampHeader != "" {
			options = append(options, WithHMACTimestampHeader(config.TimestampHeader))
		}
		return HMAC([]byte(config.Secret), options...), nil
	case TypeOAuth2:
		return OAuth2ClientCredentials(config.OAuth2)
	default:
		return nil, fmt.Errorf("auth: unknown authenticator type %q", config.Type)
	}
}
