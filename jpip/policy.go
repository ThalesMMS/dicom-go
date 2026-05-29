package jpip

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

// Rule permits one scheme and host. Port is optional; an empty Port permits
// any port on the explicitly named host. Wildcards and subdomain matching are
// intentionally unsupported.
type Rule struct {
	Scheme string
	Host   string
	Port   string
}

// BasicCredential is applied only to the exact origin in Origin. It is never
// inherited by a redirect merely because a broader Rule permits that target.
type BasicCredential struct {
	Origin   string
	Username string
	Password string
}

// BearerCredential obtains a dynamic bearer token only for the exact origin
// in Origin. Its source may refresh tokens and optionally invalidate a token
// challenged with HTTP 401.
type BearerCredential struct {
	Origin string
	Source dicomweb.BearerTokenSource
}

// Policy is an explicit network allowlist plus origin-bound credentials.
type Policy struct {
	Rules             []Rule
	Credentials       []BasicCredential
	BearerCredentials []BearerCredential
}

type normalizedRule struct {
	scheme string
	host   string
	port   string
}

type normalizedCredential struct {
	origin   string
	username string
	password string
	bearer   dicomweb.BearerTokenSource
}

type compiledPolicy struct {
	rules       []normalizedRule
	credentials map[string]normalizedCredential
}

// RuleFromURL creates an exact-origin rule from an HTTP(S) URL.
func RuleFromURL(raw string) (Rule, error) {
	u, err := parseEndpointURL(raw)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Scheme: u.Scheme, Host: u.Hostname(), Port: effectivePort(u.Scheme, u.Port())}, nil
}

// RuleForHost creates a host rule. When port is empty, every port on that
// explicitly configured host is permitted.
func RuleForHost(scheme, host, port string) (Rule, error) {
	rule := Rule{Scheme: scheme, Host: host, Port: port}
	if _, err := normalizeRule(rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func compilePolicy(policy Policy) (compiledPolicy, error) {
	type credentialKind uint8
	const (
		basicCredential credentialKind = iota + 1
		bearerCredential
	)

	out := compiledPolicy{credentials: make(map[string]normalizedCredential)}
	credentialKinds := make(map[string]credentialKind)
	for _, candidate := range policy.Rules {
		rule, err := normalizeRule(candidate)
		if err != nil {
			return compiledPolicy{}, err
		}
		duplicate := false
		for _, existing := range out.rules {
			if existing == rule {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out.rules = append(out.rules, rule)
		}
	}
	for _, candidate := range policy.Credentials {
		u, err := parseEndpointURL(candidate.Origin)
		if err != nil {
			return compiledPolicy{}, fmt.Errorf("jpip: credential origin: %w", err)
		}
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
			return compiledPolicy{}, fmt.Errorf("jpip: credential origin must not include a path, query, or fragment")
		}
		origin := canonicalOrigin(u)
		if credentialKinds[origin] == bearerCredential {
			return compiledPolicy{}, fmt.Errorf("jpip: an origin cannot use both Basic and bearer credentials")
		}
		credentialKinds[origin] = basicCredential
		out.credentials[origin] = normalizedCredential{
			origin:   origin,
			username: candidate.Username,
			password: candidate.Password,
		}
	}
	for _, candidate := range policy.BearerCredentials {
		u, err := parseEndpointURL(candidate.Origin)
		if err != nil {
			return compiledPolicy{}, fmt.Errorf("jpip: bearer credential origin: %w", err)
		}
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
			return compiledPolicy{}, fmt.Errorf("jpip: bearer credential origin must not include a path, query, or fragment")
		}
		if candidate.Source == nil {
			return compiledPolicy{}, fmt.Errorf("jpip: bearer credential source is required")
		}
		origin := canonicalOrigin(u)
		if credentialKinds[origin] == basicCredential {
			return compiledPolicy{}, fmt.Errorf("jpip: an origin cannot use both Basic and bearer credentials")
		}
		credentialKinds[origin] = bearerCredential
		out.credentials[origin] = normalizedCredential{
			origin: origin,
			bearer: candidate.Source,
		}
	}
	return out, nil
}

func normalizeRule(candidate Rule) (normalizedRule, error) {
	scheme := strings.ToLower(strings.TrimSpace(candidate.Scheme))
	if scheme != "http" && scheme != "https" {
		return normalizedRule{}, fmt.Errorf("jpip: policy scheme must be http or https")
	}
	host := strings.TrimSpace(candidate.Host)
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@") {
		return normalizedRule{}, fmt.Errorf("jpip: policy host must be a hostname or IP literal")
	}
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		if strings.TrimSpace(candidate.Port) != "" {
			return normalizedRule{}, fmt.Errorf("jpip: policy port specified twice")
		}
		host, candidate.Port = parsedHost, parsedPort
	}
	host = normalizeHostname(host)
	if host == "" {
		return normalizedRule{}, fmt.Errorf("jpip: policy host is required")
	}
	port := strings.TrimSpace(candidate.Port)
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return normalizedRule{}, fmt.Errorf("jpip: invalid policy port %q", port)
		}
		port = strconv.Itoa(number)
	}
	return normalizedRule{scheme: scheme, host: host, port: port}, nil
}

func parseEndpointURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		// net/url includes the complete input in parse errors. JPIP provider
		// URLs can carry sensitive paths and query credentials, so retain only
		// the stable error category in diagnostics.
		return nil, fmt.Errorf("jpip: parse endpoint URL: %w", ErrInvalidRequest)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("jpip: endpoint URL must use http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("jpip: endpoint URL must include a host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("jpip: endpoint URL must not contain user information")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("jpip: endpoint URL must not contain a fragment")
	}
	return u, nil
}

func (p compiledPolicy) authorize(u *url.URL) (normalizedCredential, error) {
	if u == nil {
		return normalizedCredential{}, &Error{Kind: ErrorKindInvalidRequest, Operation: "authorize", Err: ErrInvalidRequest}
	}
	if u.User != nil || u.Fragment != "" {
		return normalizedCredential{}, &Error{Kind: ErrorKindPolicyDenied, Operation: "authorize", Err: ErrPolicyDenied}
	}
	host := normalizeHostname(u.Hostname())
	scheme := strings.ToLower(u.Scheme)
	port := effectivePort(scheme, u.Port())
	for _, rule := range p.rules {
		if rule.scheme != scheme || rule.host != host ||
			rule.port != "" && effectivePort(rule.scheme, rule.port) != port {
			continue
		}
		return p.credentials[canonicalOrigin(u)], nil
	}
	return normalizedCredential{}, &Error{Kind: ErrorKindPolicyDenied, Operation: "authorize", Err: ErrPolicyDenied}
}

func effectivePort(scheme, port string) string {
	if port != "" {
		return port
	}
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func normalizeHostname(host string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
}

func canonicalOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := normalizeHostname(u.Hostname())
	port := effectivePort(u.Scheme, u.Port())
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(host, port)
}
