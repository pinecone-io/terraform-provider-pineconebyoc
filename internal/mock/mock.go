// Package mock plays the Pinecone control plane and Auth0 in memory, for tests.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
)

const (
	EnvCreate      = "env.create"
	EnvDelete      = "env.delete"
	CpgwCreate     = "cpgw.create"
	CpgwDelete     = "cpgw.delete"
	SACreate       = "sa.create"
	SADelete       = "sa.delete"
	Token          = "token"
	ProjectCreate  = "project.create"
	APIKeyCreate   = "apikey.create"
	ProjectDelete  = "project.delete"
	DatadogCreate  = "datadog.create"
	DatadogDelete  = "datadog.delete"
	DNSCreate      = "dns.create"
	DNSDelete      = "dns.delete"
	DNSWait        = "dns.wait"
	AMPCreate      = "amp.create"
	AMPDelete      = "amp.delete"
	CustomerAPIKey = "test-customer-key"
)

type Call struct {
	Route     string
	Method    string
	Path      string
	UserAgent string
	Body      map[string]any
	Status    int
}

type fault struct {
	status int
	// remaining is how many more calls fail. Negative fails until Clear.
	remaining int
}

type env struct {
	ID, Name, OrgID, OrgName string
	Domain                   *string
}

type sa struct{ ClientID, Secret string }

type Server struct {
	*httptest.Server
	// DropDomain plays a control plane that does not record a customer domain.
	DropDomain bool

	mu       sync.Mutex
	held     bool
	n        int
	calls    []Call
	faults   map[string]*fault
	envs     map[string]*env
	cpgw     map[string]string // key value -> environment name
	cpgwByID map[string]string // id -> key value
	sas      map[string]sa
	tokens   map[string]bool
	projects map[string]bool
	datadog  map[string]bool
	dns      map[string][]string
	amp      map[string]string // environment name -> workload role
}

func New() *Server {
	s := &Server{
		faults:   map[string]*fault{},
		envs:     map[string]*env{},
		cpgw:     map[string]string{},
		cpgwByID: map[string]string{},
		sas:      map[string]sa{},
		tokens:   map[string]bool{},
		projects: map[string]bool{},
		datadog:  map[string]bool{},
		dns:      map[string][]string{},
		amp:      map[string]string{},
	}
	mux := http.NewServeMux()
	const boot, infra, mgmt = "/internal/cpgw/infra/bootstrap", "/internal/cpgw/infra", "/management"
	s.handle(mux, EnvCreate, "POST "+boot+"/environments", s.customer, s.envCreate)
	s.handle(mux, EnvDelete, "DELETE "+boot+"/environments/{id}", s.customer, s.envDelete)
	s.handle(mux, CpgwCreate, "POST "+boot+"/cpgw-api-keys", s.customer, s.cpgwCreate)
	s.handle(mux, CpgwDelete, "DELETE "+boot+"/cpgw-api-keys/{id}", s.customer, s.cpgwDelete)
	s.handle(mux, SACreate, "POST "+infra+"/service-accounts", s.cpgwKey, s.saCreate)
	s.handle(mux, SADelete, "DELETE "+infra+"/service-accounts/{id}", s.cpgwKey, s.saDelete)
	s.handle(mux, Token, "POST /oauth/token", nil, s.token)
	s.handle(mux, ProjectCreate, "POST "+mgmt+"/organizations/{org}/projects", s.bearer, s.projectCreate)
	s.handle(mux, APIKeyCreate, "POST "+mgmt+"/projects/{id}/api-keys", s.bearer, s.apiKeyCreate)
	s.handle(mux, ProjectDelete, "DELETE "+mgmt+"/projects/{id}", s.bearer, s.projectDelete)
	s.handle(mux, DatadogCreate, "POST "+infra+"/datadog-credentials", s.cpgwKey, s.datadogCreate)
	s.handle(mux, DatadogDelete, "POST "+infra+"/datadog-credentials/delete", s.cpgwKey, s.datadogDelete)
	s.handle(mux, DNSCreate, "POST "+infra+"/dns-delegation", s.cpgwKey, s.dnsCreate)
	s.handle(mux, DNSDelete, "POST "+infra+"/dns-delegation/delete", s.cpgwKey, s.dnsDelete)
	s.handle(mux, AMPCreate, "POST "+infra+"/amp-access", s.cpgwKey, s.ampCreate)
	s.handle(mux, AMPDelete, "POST "+infra+"/amp-access/delete", s.cpgwKey, s.ampDelete)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, Call{Route: "unknown", Method: r.Method, Path: r.URL.Path, Status: 404})
		w.WriteHeader(http.StatusNotFound)
	})
	s.Server = httptest.NewServer(mux)
	return s
}

// Fail makes every call to route answer status until Clear.
func (s *Server) Fail(route string, status int) { s.FailTimes(route, status, -1) }

// FailTimes makes the next n calls to route answer status.
func (s *Server) FailTimes(route string, status, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[route] = &fault{status: status, remaining: n}
}

func (s *Server) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = map[string]*fault{}
}

func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// Routes lists the routes that answered 2xx, in order.
func (s *Server) Routes() []string {
	var out []string
	for _, c := range s.Calls() {
		if c.Status >= 200 && c.Status < 300 {
			out = append(out, c.Route)
		}
	}
	return out
}

func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// Live counts what exists server-side; all zero means nothing leaked.
func (s *Server) Live() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]int{
		"environments":     len(s.envs),
		"cpgw_api_keys":    len(s.cpgwByID),
		"service_accounts": len(s.sas),
		"projects":         len(s.projects),
		"datadog_keys":     len(s.datadog),
		"dns_delegations":  len(s.dns),
		"amp_access":       len(s.amp),
	}
}

func (s *Server) Delegation(subdomain string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dns[subdomain])
}

// WaitDelegated plays the parent zone: it serves the name servers the control plane holds now.
func (s *Server) WaitDelegated(ctx context.Context, fqdn string, nameServers []string) error {
	s.mu.Lock()
	got, held := slices.Clone(s.dns[strings.TrimSuffix(fqdn, ".byoc.pinecone.io")]), s.held
	served := !held && slices.Equal(got, nameServers)
	status := http.StatusOK
	if !served {
		status = http.StatusGatewayTimeout
	}
	s.calls = append(s.calls, Call{Route: DNSWait, Method: "DNS", Path: fqdn, Body: map[string]any{"nameservers": nameServers}, Status: status})
	s.mu.Unlock()
	switch {
	case held:
		<-ctx.Done()
		return fmt.Errorf("%s is not delegated: %w", fqdn, ctx.Err())
	case !served:
		return fmt.Errorf("%s is delegated to %v, not %v", fqdn, got, nameServers)
	}
	return nil
}

// HoldDelegations makes WaitDelegated wait out its deadline.
func (s *Server) HoldDelegations(hold bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = hold
}

func (s *Server) AMPRole(envName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.amp[envName]
}

func (s *Server) Environment(id string) (name string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.envs[id]
	if !ok {
		return "", false
	}
	return e.Name, true
}

type authFn func(r *http.Request) (string, bool)

type handlerFn func(w http.ResponseWriter, r *http.Request, principal string, body map[string]any)

func (s *Server) handle(mux *http.ServeMux, route, pattern string, authn authFn, fn handlerFn) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		defer s.mu.Unlock()
		rec := &recorder{ResponseWriter: w, status: 200}
		defer func() {
			s.calls = append(s.calls, Call{Route: route, Method: r.Method, Path: r.URL.Path, UserAgent: r.UserAgent(), Body: body, Status: rec.status})
		}()
		var principal string
		if authn != nil {
			p, ok := authn(r)
			if !ok {
				writeJSON(rec, http.StatusUnauthorized, map[string]any{"message": "unauthorized"})
				return
			}
			principal = p
		}
		if f, ok := s.faults[route]; ok && f.remaining != 0 {
			f.remaining--
			// Echoes the request to prove the client never surfaces anything but message.
			writeJSON(rec, f.status, map[string]any{"message": "injected fault", "request": string(raw)})
			return
		}
		fn(rec, r, principal, body)
	})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
}

func (s *Server) customer(r *http.Request) (string, bool) {
	return "customer", r.Header.Get("Api-Key") == CustomerAPIKey
}

func (s *Server) cpgwKey(r *http.Request) (string, bool) {
	name, ok := s.cpgw[r.Header.Get("Api-Key")]
	return name, ok
}

func (s *Server) bearer(r *http.Request) (string, bool) {
	jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return jwt, s.tokens[jwt] && r.Header.Get("X-Pinecone-Api-Version") == "unstable"
}

func (s *Server) next(prefix string) string {
	s.n++
	return fmt.Sprintf("%s-%d", prefix, s.n)
}

func (e *env) json(dropDomain bool) map[string]any {
	out := map[string]any{"id": e.ID, "name": e.Name, "org_id": e.OrgID, "org_name": e.OrgName}
	if e.Domain != nil && !dropDomain {
		out["domain"] = *e.Domain
	}
	return out
}

func str(body map[string]any, key string) string {
	v, _ := body[key].(string)
	return v
}

func (s *Server) envCreate(w http.ResponseWriter, _ *http.Request, _ string, body map[string]any) {
	s.n++
	e := &env{
		ID:      fmt.Sprintf("env-%d", s.n),
		Name:    fmt.Sprintf("%s-%s-%04x.byoc", str(body, "cloud"), str(body, "region"), s.n),
		OrgID:   "org-1",
		OrgName: "Acme Corp",
	}
	if d, ok := body["domain"].(string); ok {
		e.Domain = &d
	}
	s.envs[e.ID] = e
	writeJSON(w, http.StatusOK, e.json(s.DropDomain))
}

func (s *Server) envDelete(w http.ResponseWriter, r *http.Request, _ string, _ map[string]any) {
	if _, ok := s.envs[r.PathValue("id")]; !ok {
		notFound(w)
		return
	}
	delete(s.envs, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) cpgwCreate(w http.ResponseWriter, _ *http.Request, _ string, body map[string]any) {
	name := str(body, "environment")
	known := false
	for _, e := range s.envs {
		known = known || e.Name == name
	}
	if !known {
		notFound(w)
		return
	}
	id, key := s.next("cpgw-id"), s.next("cpgw-k")
	s.cpgw[key], s.cpgwByID[id] = name, key
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "environment": name, "key": key})
}

func (s *Server) cpgwDelete(w http.ResponseWriter, r *http.Request, _ string, _ map[string]any) {
	key, ok := s.cpgwByID[r.PathValue("id")]
	if !ok {
		notFound(w)
		return
	}
	delete(s.cpgwByID, r.PathValue("id"))
	delete(s.cpgw, key)
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) saCreate(w http.ResponseWriter, _ *http.Request, _ string, _ map[string]any) {
	id := s.next("sa")
	acct := sa{ClientID: s.next("client"), Secret: s.next("sa-s")}
	s.sas[id] = acct
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "client_id": acct.ClientID, "client_secret": acct.Secret})
}

func (s *Server) saDelete(w http.ResponseWriter, r *http.Request, _ string, _ map[string]any) {
	if _, ok := s.sas[r.PathValue("id")]; !ok {
		notFound(w)
		return
	}
	delete(s.sas, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) token(w http.ResponseWriter, _ *http.Request, _ string, body map[string]any) {
	if str(body, "audience") != s.URL+"/" || str(body, "grant_type") != "client_credentials" {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "bad audience"})
		return
	}
	for _, acct := range s.sas {
		if acct.ClientID == str(body, "client_id") && acct.Secret == str(body, "client_secret") {
			jwt := s.next("jwt")
			s.tokens[jwt] = true
			writeJSON(w, http.StatusOK, map[string]any{"access_token": jwt})
			return
		}
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "access_denied"})
}

func (s *Server) projectCreate(w http.ResponseWriter, _ *http.Request, _ string, _ map[string]any) {
	id := s.next("proj")
	s.projects[id] = true
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) apiKeyCreate(w http.ResponseWriter, r *http.Request, _ string, _ map[string]any) {
	if !s.projects[r.PathValue("id")] {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":   map[string]any{"id": s.next("key"), "project_id": r.PathValue("id")},
		"value": s.next("sli-k"),
	})
}

func (s *Server) projectDelete(w http.ResponseWriter, r *http.Request, _ string, _ map[string]any) {
	if !s.projects[r.PathValue("id")] {
		notFound(w)
		return
	}
	delete(s.projects, r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) datadogCreate(w http.ResponseWriter, _ *http.Request, _ string, _ map[string]any) {
	id := s.next("dd-id")
	s.datadog[id] = true
	writeJSON(w, http.StatusOK, map[string]any{"api_key": s.next("dd-k"), "key_id": id})
}

func (s *Server) datadogDelete(w http.ResponseWriter, _ *http.Request, _ string, body map[string]any) {
	if !s.datadog[str(body, "key_id")] {
		notFound(w)
		return
	}
	delete(s.datadog, str(body, "key_id"))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func nameservers(body map[string]any) []string {
	raw, _ := body["nameservers"].([]any)
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) dnsCreate(w http.ResponseWriter, _ *http.Request, _ string, body map[string]any) {
	sub := str(body, "subdomain")
	s.dns[sub] = nameservers(body)
	writeJSON(w, http.StatusOK, map[string]any{"change_id": s.next("change"), "status": "PENDING", "fqdn": sub + ".byoc.pinecone.io"})
}

func (s *Server) dnsDelete(w http.ResponseWriter, _ *http.Request, _ string, body map[string]any) {
	sub := str(body, "subdomain")
	if _, ok := s.dns[sub]; !ok {
		notFound(w)
		return
	}
	delete(s.dns, sub)
	writeJSON(w, http.StatusOK, map[string]any{"change_id": s.next("change"), "status": "PENDING"})
}

func (s *Server) ampCreate(w http.ResponseWriter, _ *http.Request, envName string, body map[string]any) {
	s.amp[envName] = str(body, "workload_role_arn")
	writeJSON(w, http.StatusOK, map[string]any{
		"pinecone_role_arn":         "arn:aws:iam::111122223333:role/amp-writer",
		"amp_remote_write_endpoint": "https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-test/api/v1/remote_write",
		"amp_region":                "us-east-1",
	})
}

func (s *Server) ampDelete(w http.ResponseWriter, _ *http.Request, envName string, _ map[string]any) {
	if _, ok := s.amp[envName]; !ok {
		notFound(w)
		return
	}
	delete(s.amp, envName)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
