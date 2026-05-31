package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Config is the merged, effective configuration returned by ResolveConfig:
// global < service < version layered into one flat map, with typed accessors.
type Config struct {
	Service    string
	Version    string
	Vars       map[string]TypedValue
	Provenance map[string]string // key -> scope ID that provided it
	// ETag is the aggregate hash of exactly this query's result. Compare it to a
	// previously-seen value (or use ConfigETag) to detect changes cheaply.
	ETag string
}

func (c *Client) resolvePath(service, version string, opts []ResolveOption) string {
	q := url.Values{}
	if service != "" {
		q.Set("service", service)
	}
	if version != "" {
		q.Set("version", version)
	}
	for _, opt := range opts {
		opt(q)
	}
	path := "/v1/config/resolve"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return path
}

// ResolveConfig fetches the effective config for a (service, version). Optional
// prefixes/keys restrict the result (a key is returned if it equals any key or
// starts with any prefix). Pass version "" to skip version-block resolution.
// With a cache backend (WithCache) this revalidates cheaply and survives
// discovery downtime.
func (c *Client) ResolveConfig(ctx context.Context, service, version string, opts ...ResolveOption) (*Config, error) {
	body, etag, err := c.doGet(ctx, c.resolvePath(service, version, opts))
	if err != nil {
		return nil, err
	}
	var rc resolvedConfig
	if err := json.Unmarshal(body, &rc); err != nil {
		return nil, err
	}
	if etag == "" {
		etag = rc.ETag
	}
	return &Config{Service: rc.Service, Version: rc.Version, Vars: rc.Vars, Provenance: rc.Provenance, ETag: etag}, nil
}

// ConfigETag returns just the aggregate ETag for a resolve query via a HEAD
// request (no body). Poll it cheaply and only ResolveConfig when it changes
// from the ETag you last saw.
func (c *Client) ConfigETag(ctx context.Context, service, version string, opts ...ResolveOption) (string, error) {
	resp, err := c.do(ctx, http.MethodHead, c.resolvePath(service, version, opts), nil)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("discovery: %s", resp.Status)
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

// ResolveOption tunes a ResolveConfig query.
type ResolveOption func(url.Values)

// WithPrefixes restricts the resolved config to keys under any of the prefixes.
func WithPrefixes(prefixes ...string) ResolveOption {
	return func(q url.Values) {
		for _, p := range prefixes {
			q.Add("prefix", p)
		}
	}
}

// WithKeys restricts the resolved config to the listed keys.
func WithKeys(keys ...string) ResolveOption {
	return func(q url.Values) {
		for _, k := range keys {
			q.Add("key", k)
		}
	}
}

// --- typed accessors ---

// Raw returns the typed value for a key.
func (c *Config) Raw(key string) (TypedValue, bool) { v, ok := c.Vars[key]; return v, ok }

// Keys returns all keys present in the resolved config.
func (c *Config) Keys() []string {
	out := make([]string, 0, len(c.Vars))
	for k := range c.Vars {
		out = append(out, k)
	}
	return out
}

// String returns the value as a string (works for string vars; for others it
// falls back to the raw JSON text).
func (c *Config) String(key string) (string, bool) {
	v, ok := c.Vars[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s, true
	}
	return string(v.Value), true
}

// Int returns the value as int64.
func (c *Config) Int(key string) (int64, bool) {
	v, ok := c.Vars[key]
	if !ok {
		return 0, false
	}
	var n int64
	if json.Unmarshal(v.Value, &n) == nil {
		return n, true
	}
	// tolerate a numeric string
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// Float returns the value as float64.
func (c *Config) Float(key string) (float64, bool) {
	v, ok := c.Vars[key]
	if !ok {
		return 0, false
	}
	var f float64
	if json.Unmarshal(v.Value, &f) == nil {
		return f, true
	}
	return 0, false
}

// Bool returns the value as bool.
func (c *Config) Bool(key string) (bool, bool) {
	v, ok := c.Vars[key]
	if !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal(v.Value, &b) == nil {
		return b, true
	}
	return false, false
}

// Bytes decodes a bytes var (base64) into raw bytes.
func (c *Config) Bytes(key string) ([]byte, bool) {
	v, ok := c.Vars[key]
	if !ok {
		return nil, false
	}
	var s string
	if json.Unmarshal(v.Value, &s) != nil {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

// JSON unmarshals a json var (or any value) into out.
func (c *Config) JSON(key string, out any) error {
	v, ok := c.Vars[key]
	if !ok {
		return fmt.Errorf("config: key %q not found", key)
	}
	return json.Unmarshal(v.Value, out)
}

// --- writes ---

// ApplyConfig publishes vars as a new revision of a scope (atomic block
// replace). Requires write access to the scope (global → admin).
func (c *Client) ApplyConfig(ctx context.Context, scope ConfigScope, vars map[string]TypedValue, note string) (*ConfigRevision, error) {
	body := map[string]any{"scope": scope, "vars": vars, "note": note}
	resp, err := c.do(ctx, http.MethodPost, "/v1/config/apply", body)
	if err != nil {
		return nil, err
	}
	var rev ConfigRevision
	return &rev, decodeJSON(resp, &rev)
}

// SetGlobalConfig replaces the global config block.
func (c *Client) SetGlobalConfig(ctx context.Context, vars map[string]TypedValue, note string) (*ConfigRevision, error) {
	return c.ApplyConfig(ctx, ConfigScope{Kind: ScopeGlobal}, vars, note)
}

// SetServiceConfig replaces a service's config block.
func (c *Client) SetServiceConfig(ctx context.Context, service string, vars map[string]TypedValue, note string) (*ConfigRevision, error) {
	return c.ApplyConfig(ctx, ConfigScope{Kind: ScopeService, Service: service}, vars, note)
}

// SetVersionConfig replaces a service's version-constrained config block
// (constraint is npm-style semver, e.g. ">=2.1.0").
func (c *Client) SetVersionConfig(ctx context.Context, service, constraint string, vars map[string]TypedValue, note string) (*ConfigRevision, error) {
	return c.ApplyConfig(ctx, ConfigScope{Kind: ScopeVersion, Service: service, Constraint: constraint}, vars, note)
}

// --- typed value builders ---

func StringVar(s string) TypedValue { b, _ := json.Marshal(s); return TypedValue{VarString, b} }
func IntVar(n int64) TypedValue     { b, _ := json.Marshal(n); return TypedValue{VarInt, b} }
func FloatVar(f float64) TypedValue { b, _ := json.Marshal(f); return TypedValue{VarFloat, b} }
func BoolVar(v bool) TypedValue     { b, _ := json.Marshal(v); return TypedValue{VarBool, b} }
func BytesVar(raw []byte) TypedValue {
	b, _ := json.Marshal(base64.StdEncoding.EncodeToString(raw))
	return TypedValue{VarBytes, b}
}

// JSONVar wraps an arbitrary value as a json var.
func JSONVar(v any) (TypedValue, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return TypedValue{}, err
	}
	return TypedValue{VarJSON, b}, nil
}
