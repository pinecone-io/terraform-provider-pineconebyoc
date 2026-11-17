// Package client makes the Pinecone BYOC registration calls. Routes, request and response fields
// follow the API exactly.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const HostedDomain = "pinecone.io"

type Client struct {
	APIURL      string
	Auth0Domain string
	// The customer's Pinecone API key. Only the bootstrap routes take it.
	APIKey     string
	GlobalEnv  string
	GCPProject string
	UserAgent  string
	HTTP       *http.Client

	// Every 5xx is retried three times, after 2, 4 and 8 seconds.
	Retries        int
	RetryBaseDelay time.Duration
}

type APIError struct {
	Status  int
	Route   string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Route, e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Route, e.Status, e.Message)
}

func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func IsServerError(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status >= 500 && e.Status <= 599
}

func ignoreNotFound(err error) error {
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) Backoff(ctx context.Context, attempt int) error {
	t := time.NewTimer(c.RetryBaseDelay << attempt)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type auth func(*http.Request)

func apiKey(key string) auth {
	return func(r *http.Request) { r.Header.Set("Api-Key", key) }
}

func bearer(jwt string) auth {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+jwt)
		r.Header.Set("X-Pinecone-Api-Version", "unstable")
	}
}

func (c *Client) bootstrapURL() string { return c.APIURL + "/internal/cpgw/infra/bootstrap" }
func (c *Client) infraURL() string     { return c.APIURL + "/internal/cpgw/infra" }
func (c *Client) mgmtURL() string      { return c.APIURL + "/management" }

func (c *Client) do(ctx context.Context, method, url string, a auth, in, out any) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = c.once(ctx, method, url, a, in, out)
		if !IsServerError(err) || attempt >= c.Retries {
			return err
		}
		if werr := c.Backoff(ctx, attempt); werr != nil {
			return fmt.Errorf("%w, waiting to retry after %v", werr, err)
		}
	}
}

func (c *Client) once(ctx context.Context, method, url string, a auth, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if a != nil {
		a(req)
	}
	hc := http.Client{Timeout: time.Minute}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	// Go forwards Api-Key and the request body to another host on a redirect. Pinecone's API never redirects.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	route := method + " " + strings.TrimPrefix(url, c.APIURL)
	resp, err := hc.Do(req)
	if err != nil {
		// The transport error can carry the URL but never a header, so it holds no credential.
		return fmt.Errorf("%s: %w", route, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Route: route, Message: errorMessage(raw)}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: invalid response", route)
		}
	}
	return nil
}

// errorMessage keeps only the server's message field: a raw body can echo the request.
func errorMessage(raw []byte) string {
	var v struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	msg := v.Message
	if msg == "" {
		if s, ok := v.Error.(string); ok {
			msg = s
		} else if m, ok := v.Error.(map[string]any); ok {
			msg, _ = m["message"].(string)
		}
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func missing(route string, fields map[string]string) error {
	for name, v := range fields {
		if v == "" {
			return fmt.Errorf("%s: invalid response: %s missing", route, name)
		}
	}
	return nil
}

type CreateEnvironmentRequest struct {
	Cloud                   string  `json:"cloud"`
	Region                  string  `json:"region"`
	GlobalEnv               string  `json:"global_env"`
	IsPublicEndpointEnabled bool    `json:"is_public_endpoint_enabled"`
	Domain                  *string `json:"domain,omitempty"`
}

type Environment struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	OrgID   string  `json:"org_id"`
	OrgName string  `json:"org_name"`
	Domain  *string `json:"domain"`
}

func (c *Client) CreateEnvironment(ctx context.Context, in CreateEnvironmentRequest) (Environment, error) {
	var env Environment
	if err := c.do(ctx, http.MethodPost, c.bootstrapURL()+"/environments", apiKey(c.APIKey), in, &env); err != nil {
		return env, err
	}
	if err := missing("create environment", map[string]string{"id": env.ID, "name": env.Name, "org_id": env.OrgID, "org_name": env.OrgName}); err != nil {
		return env, err
	}
	// A control plane that drops the domain advertises index hosts under pinecone.io, which nothing serves.
	if in.Domain != nil && (env.Domain == nil || *env.Domain != *in.Domain) {
		got := "none"
		if env.Domain != nil {
			got = *env.Domain
		}
		return env, fmt.Errorf("control plane recorded domain %q, expected %q: point api_url at a control plane that records one", got, *in.Domain)
	}
	return env, nil
}

// DeleteEnvironment counts an absent environment as deleted: the control plane answers 200 for one.
func (c *Client) DeleteEnvironment(ctx context.Context, id string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, c.bootstrapURL()+"/environments/"+id, apiKey(c.APIKey), nil, nil))
}

type CpgwAPIKey struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Key         string `json:"key"`
}

// CreateCpgwAPIKey takes the environment's name, not its id: the API's field is the name.
func (c *Client) CreateCpgwAPIKey(ctx context.Context, envName string) (CpgwAPIKey, error) {
	var k CpgwAPIKey
	if err := c.do(ctx, http.MethodPost, c.bootstrapURL()+"/cpgw-api-keys", apiKey(c.APIKey), map[string]string{"environment": envName}, &k); err != nil {
		return k, err
	}
	return k, missing("create cpgw api key", map[string]string{"id": k.ID, "key": k.Key})
}

func (c *Client) DeleteCpgwAPIKey(ctx context.Context, id string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, c.bootstrapURL()+"/cpgw-api-keys/"+id, apiKey(c.APIKey), nil, nil))
}

type ServiceAccount struct {
	ID           string `json:"id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (c *Client) CreateServiceAccount(ctx context.Context, cpgwKey, name string) (ServiceAccount, error) {
	var sa ServiceAccount
	if err := c.do(ctx, http.MethodPost, c.infraURL()+"/service-accounts", apiKey(cpgwKey), map[string]string{"name": name}, &sa); err != nil {
		return sa, err
	}
	return sa, missing("create service account", map[string]string{"id": sa.ID, "client_id": sa.ClientID, "client_secret": sa.ClientSecret})
}

func (c *Client) DeleteServiceAccount(ctx context.Context, cpgwKey, id string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, c.infraURL()+"/service-accounts/"+id, apiKey(cpgwKey), nil, nil))
}

// Token is never retried, and never reports the response body: the request carries the client
// secret.
func (c *Client) Token(ctx context.Context, clientID, clientSecret string) (string, error) {
	in := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"audience":      c.APIURL + "/",
		"grant_type":    "client_credentials",
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	err := c.once(ctx, http.MethodPost, c.Auth0Domain+"/oauth/token", func(r *http.Request) { r.Header.Set("cache-control", "no-cache") }, in, &out)
	var e *APIError
	if errors.As(err, &e) {
		return "", &APIError{Status: e.Status, Route: "POST /oauth/token"}
	}
	if err != nil {
		return "", errors.New("POST /oauth/token failed")
	}
	if out.AccessToken == "" {
		return "", errors.New("POST /oauth/token: no access_token")
	}
	return out.AccessToken, nil
}

func (c *Client) CreateProject(ctx context.Context, jwt, orgID, name string) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, c.mgmtURL()+"/organizations/"+orgID+"/projects", bearer(jwt), map[string]string{"name": name}, &p); err != nil {
		return "", err
	}
	return p.ID, missing("create project", map[string]string{"id": p.ID})
}

type ProjectAPIKey struct {
	Key struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	} `json:"key"`
	Value string `json:"value"`
}

func (c *Client) CreateProjectAPIKey(ctx context.Context, jwt, projectID, name string) (ProjectAPIKey, error) {
	var k ProjectAPIKey
	body := map[string]any{"name": name, "roles": []string{"ProjectEditor"}}
	if err := c.do(ctx, http.MethodPost, c.mgmtURL()+"/projects/"+projectID+"/api-keys", bearer(jwt), body, &k); err != nil {
		return k, err
	}
	return k, missing("create api key", map[string]string{"key.id": k.Key.ID, "value": k.Value})
}

func (c *Client) DeleteProject(ctx context.Context, jwt, projectID string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, c.mgmtURL()+"/projects/"+projectID, bearer(jwt), nil, nil))
}

type DatadogKey struct {
	APIKey string `json:"api_key"`
	KeyID  string `json:"key_id"`
}

func (c *Client) CreateDatadogKey(ctx context.Context, cpgwKey string) (DatadogKey, error) {
	var k DatadogKey
	if err := c.do(ctx, http.MethodPost, c.infraURL()+"/datadog-credentials", apiKey(cpgwKey), nil, &k); err != nil {
		return k, err
	}
	return k, missing("create datadog key", map[string]string{"api_key": k.APIKey, "key_id": k.KeyID})
}

func (c *Client) DeleteDatadogKey(ctx context.Context, cpgwKey, keyID string) error {
	return ignoreNotFound(c.do(ctx, http.MethodPost, c.infraURL()+"/datadog-credentials/delete", apiKey(cpgwKey), map[string]string{"key_id": keyID}, nil))
}

type DNSDelegation struct {
	ChangeID string `json:"change_id"`
	Status   string `json:"status"`
	FQDN     string `json:"fqdn"`
}

type dnsDelegationRequest struct {
	Subdomain   string   `json:"subdomain"`
	Nameservers []string `json:"nameservers"`
}

// CreateDNSDelegation is an upsert: an update posts it again with the new name servers.
func (c *Client) CreateDNSDelegation(ctx context.Context, cpgwKey, subdomain string, nameservers []string) (DNSDelegation, error) {
	var d DNSDelegation
	if err := c.do(ctx, http.MethodPost, c.infraURL()+"/dns-delegation", apiKey(cpgwKey), dnsDelegationRequest{subdomain, nameservers}, &d); err != nil {
		return d, err
	}
	d.FQDN = strings.TrimSuffix(d.FQDN, ".")
	return d, missing("create dns delegation", map[string]string{"fqdn": d.FQDN})
}

func (c *Client) DeleteDNSDelegation(ctx context.Context, cpgwKey, subdomain string, nameservers []string) error {
	return ignoreNotFound(c.do(ctx, http.MethodPost, c.infraURL()+"/dns-delegation/delete", apiKey(cpgwKey), dnsDelegationRequest{subdomain, nameservers}, nil))
}

type AMPAccess struct {
	PineconeRoleARN        string `json:"pinecone_role_arn"`
	AMPRemoteWriteEndpoint string `json:"amp_remote_write_endpoint"`
	AMPRegion              string `json:"amp_region"`
}

// CreateAMPAccess is an upsert: an update posts it again with the new role.
func (c *Client) CreateAMPAccess(ctx context.Context, cpgwKey, workloadRoleARN string) (AMPAccess, error) {
	var a AMPAccess
	if err := c.do(ctx, http.MethodPost, c.infraURL()+"/amp-access", apiKey(cpgwKey), map[string]string{"workload_role_arn": workloadRoleARN}, &a); err != nil {
		return a, err
	}
	return a, missing("create amp access", map[string]string{"pinecone_role_arn": a.PineconeRoleARN, "amp_remote_write_endpoint": a.AMPRemoteWriteEndpoint, "amp_region": a.AMPRegion})
}

func (c *Client) DeleteAMPAccess(ctx context.Context, cpgwKey string) error {
	return ignoreNotFound(c.do(ctx, http.MethodPost, c.infraURL()+"/amp-access/delete", apiKey(cpgwKey), nil, nil))
}
