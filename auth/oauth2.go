package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// OAuth2Config configures the OAuth2 client-credentials grant.
//
// The token endpoint is called with HTTP basic authentication carrying
// ClientID and ClientSecret, an application/x-www-form-urlencoded body with
// grant_type=client_credentials and, when configured, a space separated scope.
type OAuth2Config struct {
	// TokenURL is the absolute http or https URL of the token endpoint.
	TokenURL string `mapstructure:"tokenURL" json:"tokenURL" yaml:"tokenURL" toml:"tokenURL"`
	// ClientID is the OAuth2 client identifier.
	ClientID string `mapstructure:"clientID" json:"clientID" yaml:"clientID" toml:"clientID"`
	// ClientSecret is the OAuth2 client secret.
	ClientSecret string `mapstructure:"clientSecret" json:"clientSecret" yaml:"clientSecret" toml:"clientSecret"`
	// Scopes are requested as a single space separated scope parameter.
	Scopes []string `mapstructure:"scopes" json:"scopes" yaml:"scopes" toml:"scopes"`
	// HTTPClient overrides the client used for token requests. It is not
	// decoded from configuration documents; a client with a short timeout is
	// used when it is nil.
	HTTPClient *http.Client `mapstructure:"-" json:"-" yaml:"-" toml:"-"`
}

const (
	oauth2DefaultTimeout    = 10 * time.Second
	oauth2MaxResponseBytes  = 1 << 20
	oauth2ErrorExcerptBytes = 256
	oauth2MinRefreshSkew    = time.Second
	oauth2MaxRefreshSkew    = 30 * time.Second
)

// oauth2Token is an access token together with the metadata needed to decide
// when it should be replaced.
type oauth2Token struct {
	value     string
	tokenType string
	expiresIn time.Duration
}

// oauth2Call is a token request that other requests can wait for. Closing done
// publishes token and err to every waiter.
type oauth2Call struct {
	done  chan struct{}
	token oauth2Token
	err   error
}

// oauth2Authenticator adds a cached bearer token to requests. Concurrent
// requests share a single token request.
type oauth2Authenticator struct {
	config OAuth2Config
	client *http.Client
	now    func() time.Time

	mutex     sync.Mutex
	token     oauth2Token
	refreshAt time.Time
	hasExpiry bool
	inflight  *oauth2Call
}

// OAuth2ClientCredentials returns an authenticator that acquires access tokens
// with the OAuth2 client-credentials grant.
//
// Tokens are cached until shortly before they expire. Requests that arrive
// while a token is being fetched wait for that single in-flight request, and
// a failed fetch is returned to every waiter without being cached.
func OAuth2ClientCredentials(config OAuth2Config) (Authenticator, error) {
	if err := validateOAuth2Config(config); err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: oauth2DefaultTimeout}
	}
	config.Scopes = append([]string(nil), config.Scopes...)
	return &oauth2Authenticator{
		config: config,
		client: client,
		now:    time.Now,
	}, nil
}

func validateOAuth2Config(config OAuth2Config) error {
	if strings.TrimSpace(config.TokenURL) == "" {
		return fmt.Errorf("auth: oauth2: token URL is required")
	}
	parsed, err := url.Parse(config.TokenURL)
	if err != nil {
		return fmt.Errorf("auth: oauth2: invalid token URL %q: %w", config.TokenURL, err)
	}
	if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("auth: oauth2: invalid token URL %q", config.TokenURL)
	}
	if config.ClientID == "" {
		return fmt.Errorf("auth: oauth2: client ID is required")
	}
	if config.ClientSecret == "" {
		return fmt.Errorf("auth: oauth2: client secret is required")
	}
	return nil
}

// Apply implements Authenticator.
func (a *oauth2Authenticator) Apply(request *http.Request) error {
	token, err := a.accessToken(request.Context())
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", token.tokenType+" "+token.value)
	return nil
}

// accessToken returns a valid token, refreshing or fetching it when necessary.
// The first caller performs the request with its request context; later
// callers wait for its result so a burst of requests triggers one token
// request. Waiters give up when their own context is cancelled.
func (a *oauth2Authenticator) accessToken(ctx context.Context) (oauth2Token, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	a.mutex.Lock()
	if a.token.value != "" && (!a.hasExpiry || a.now().Before(a.refreshAt)) {
		token := a.token
		a.mutex.Unlock()
		return token, nil
	}
	if call := a.inflight; call != nil {
		a.mutex.Unlock()
		select {
		case <-call.done:
			return call.token, call.err
		case <-ctx.Done():
			return oauth2Token{}, ctx.Err()
		}
	}
	call := &oauth2Call{done: make(chan struct{})}
	a.inflight = call
	a.mutex.Unlock()

	token, err := a.fetch(ctx)

	a.mutex.Lock()
	now := a.now()
	if err == nil {
		a.token = token
		a.hasExpiry = token.expiresIn > 0
		if a.hasExpiry {
			a.refreshAt = now.Add(token.expiresIn - oauth2RefreshSkew(token.expiresIn))
		} else {
			a.refreshAt = time.Time{}
		}
	}
	call.token = token
	call.err = err
	a.inflight = nil
	close(call.done)
	a.mutex.Unlock()

	return token, err
}

// fetch performs the client-credentials token request.
func (a *oauth2Authenticator) fetch(ctx context.Context) (oauth2Token, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	if len(a.config.Scopes) > 0 {
		form.Set("scope", strings.Join(a.config.Scopes, " "))
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauth2Token{}, fmt.Errorf("auth: oauth2: token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)

	response, err := a.client.Do(request)
	if err != nil {
		return oauth2Token{}, fmt.Errorf("auth: oauth2: token request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		excerpt := oauth2ErrorExcerpt(response.Body)
		if excerpt == "" {
			return oauth2Token{}, fmt.Errorf("auth: oauth2: token endpoint returned %s", response.Status)
		}
		return oauth2Token{}, fmt.Errorf("auth: oauth2: token endpoint returned %s: %s", response.Status, excerpt)
	}

	var payload oauth2TokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, oauth2MaxResponseBytes)).Decode(&payload); err != nil {
		return oauth2Token{}, fmt.Errorf("auth: oauth2: decode token response: %w", err)
	}
	if payload.Error != "" {
		return oauth2Token{}, fmt.Errorf("auth: oauth2: token endpoint error %q: %s", payload.Error, payload.ErrorDescription)
	}
	if payload.AccessToken == "" {
		return oauth2Token{}, fmt.Errorf("auth: oauth2: token response has no access token")
	}

	expiresIn := time.Duration(0)
	if payload.ExpiresIn > 0 {
		expiresIn = time.Duration(payload.ExpiresIn) * time.Second
	}
	return oauth2Token{
		value:     payload.AccessToken,
		tokenType: canonicalTokenType(payload.TokenType),
		expiresIn: expiresIn,
	}, nil
}

// oauth2TokenResponse is the subset of a token response this package uses.
type oauth2TokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// oauth2RefreshSkew returns how long before expiry a token is replaced: ten
// percent of its lifetime, kept between one and thirty seconds and always
// smaller than the lifetime itself.
func oauth2RefreshSkew(lifetime time.Duration) time.Duration {
	skew := lifetime / 10
	if skew < oauth2MinRefreshSkew {
		skew = oauth2MinRefreshSkew
	}
	if skew > oauth2MaxRefreshSkew {
		skew = oauth2MaxRefreshSkew
	}
	if skew >= lifetime {
		skew = lifetime / 2
	}
	return skew
}

// canonicalTokenType normalises the token type sent in the Authorization
// header. Servers commonly return "bearer" while the header convention is
// "Bearer".
func canonicalTokenType(tokenType string) string {
	if tokenType == "" {
		return "Bearer"
	}
	first, size := utf8.DecodeRuneInString(tokenType)
	if first == utf8.RuneError {
		return "Bearer"
	}
	return string(unicode.ToUpper(first)) + tokenType[size:]
}

// oauth2ErrorExcerpt returns a bounded, single-line excerpt of an error body.
func oauth2ErrorExcerpt(body io.Reader) string {
	if body == nil {
		return ""
	}
	buffer := make([]byte, oauth2ErrorExcerptBytes+1)
	read, err := io.ReadFull(body, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return ""
	}
	truncated := read > oauth2ErrorExcerptBytes
	if truncated {
		read = oauth2ErrorExcerptBytes
	}
	excerpt := strings.Join(strings.Fields(string(buffer[:read])), " ")
	if truncated && excerpt != "" {
		excerpt += "..."
	}
	return excerpt
}
