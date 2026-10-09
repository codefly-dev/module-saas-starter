// Package operations validates the host-owned declarations for a callable source.
// Callers supply one input object; only the admitted declaration selects a route.
package operations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Allow both output JSON strings, including worst-case escaping, inside the
// Runnable one-MiB envelope. The transport itself retains its five-MiB ceiling.
const MaxOutputBytes = 64 * 1024
const MaxInputBytes = 65536
const (
	ReadOnly = "READ_ONLY"
	Mutation = "MUTATION"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var parameterPattern = regexp.MustCompile(`\{([a-z][a-z0-9_]{0,63})\}`)

// Declaration is persisted atomically as part of a source's operation set.
// Digest covers all fields, including the route that is not exposed to tools.
type Declaration struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Method         string          `json:"method"`
	Path           string          `json:"path"`
	Query          []string        `json:"query,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema"`
	Effect         string          `json:"effect"`
	MaxOutputBytes int             `json:"max_output_bytes"`
	Digest         string          `json:"digest,omitempty"`
}

// Refusal never retains input values or an upstream error. Pointer is derived
// only from schema-declared property names, never an unrecognised input key.
type Refusal struct{ Reason, Pointer string }

func (e *Refusal) Error() string          { return "source operation: " + e.Reason + " at " + e.Pointer }
func refuse(reason, pointer string) error { return &Refusal{reason, pointer} }

func object(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, refuse("object required", "/")
	}
	// A second JSON value is never admitted.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, refuse("one object required", "/")
	}
	return out, nil
}

// Reject network and dynamic references at declaration admission. Schema
// compilation must not create another provider-fetch or credential path.
func localSchema(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if k == "$ref" || k == "$dynamicRef" || k == "$recursiveRef" || k == "$id" {
				return false
			}
			if !localSchema(x) {
				return false
			}
		}
	case []any:
		for _, x := range v {
			if !localSchema(x) {
				return false
			}
		}
	}
	return true
}
func compile(data []byte, input bool) (*jsonschema.Schema, map[string]any, error) {
	if len(data) > MaxInputBytes {
		return nil, nil, refuse("schema exceeds bound", "/")
	}
	obj, err := object(data)
	if err != nil || obj["type"] != "object" || !localSchema(obj) {
		return nil, nil, refuse("unsupported schema", "/")
	}
	if input && obj["additionalProperties"] != false {
		return nil, nil, refuse("input schema must close additional properties", "/")
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noExternalSchema{})
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("https://schema.example.com/operation", obj); err != nil {
		return nil, nil, refuse("invalid schema", "/")
	}
	schema, err := c.Compile("https://schema.example.com/operation")
	if err != nil {
		return nil, nil, refuse("invalid schema", "/")
	}
	return schema, obj, nil
}

// Even an unknown $schema URI cannot cause network or local-file access.
type noExternalSchema struct{}

func (noExternalSchema) Load(string) (any, error) {
	return nil, refuse("external schema loading forbidden", "/")
}

// Admit validates and canonicalises a declaration. A supplied digest must
// match; callers cannot replace the route while retaining its admitted identity.
func Admit(in Declaration) (Declaration, error) {
	if !namePattern.MatchString(in.Name) {
		return Declaration{}, refuse("invalid name", "/name")
	}
	if len(in.Description) > 2048 {
		return Declaration{}, refuse("description exceeds bound", "/description")
	}
	switch in.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return Declaration{}, refuse("unsupported method", "/method")
	}
	if in.Effect != ReadOnly && in.Effect != Mutation {
		return Declaration{}, refuse("effect required", "/effect")
	}
	if in.MaxOutputBytes < 1 || in.MaxOutputBytes > MaxOutputBytes {
		return Declaration{}, refuse("invalid output bound", "/max_output_bytes")
	}
	if len(in.Path) > 2048 || !strings.HasPrefix(in.Path, "/") || strings.HasPrefix(in.Path, "//") || strings.ContainsAny(in.Path, "?#\\\r\n") {
		return Declaration{}, refuse("invalid path template", "/path")
	}
	stripped := parameterPattern.ReplaceAllString(in.Path, "param")
	if strings.ContainsAny(stripped, "{}") {
		return Declaration{}, refuse("invalid path parameter", "/path")
	}
	for _, seg := range strings.Split(stripped, "/") {
		if seg == "." || seg == ".." || strings.Contains(seg, "%") {
			return Declaration{}, refuse("invalid path segment", "/path")
		}
	}
	_, input, err := compile(in.InputSchema, true)
	if err != nil {
		return Declaration{}, err
	}
	_, output, err := compile(in.OutputSchema, false)
	if err != nil {
		return Declaration{}, err
	}
	properties, _ := input["properties"].(map[string]any)
	used := map[string]bool{}
	for _, p := range parameterPattern.FindAllStringSubmatch(in.Path, -1) {
		if _, ok := properties[p[1]]; !ok {
			return Declaration{}, refuse("path parameter is not declared", "/path")
		}
		used[p[1]] = true
	}
	query := append([]string(nil), in.Query...)
	sort.Strings(query)
	for _, q := range query {
		if !namePattern.MatchString(q) || used[q] {
			return Declaration{}, refuse("invalid query mapping", "/query")
		}
		if _, ok := properties[q]; !ok {
			return Declaration{}, refuse("query field is not declared", "/query")
		}
		used[q] = true
	}
	in.Query = query
	in.InputSchema, _ = json.Marshal(input)
	in.OutputSchema, _ = json.Marshal(output)
	want := in.Digest
	in.Digest = ""
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	in.Digest = "sha256:" + hex.EncodeToString(sum[:])
	if want != "" && want != in.Digest {
		return Declaration{}, refuse("declaration digest mismatch", "/digest")
	}
	return in, nil
}

func validate(data []byte, schemaData []byte, input bool) (map[string]any, error) {
	schema, obj, err := compile(schemaData, input)
	if err != nil {
		return nil, err
	}
	value, err := object(data)
	if err != nil {
		return nil, err
	}
	if err = schema.Validate(value); err != nil {
		pointer := "/"
		var validation *jsonschema.ValidationError
		if errors.As(err, &validation) {
			for len(validation.Causes) > 0 {
				validation = validation.Causes[0]
			}
			properties, _ := obj["properties"].(map[string]any)
			if len(validation.InstanceLocation) > 0 {
				name := validation.InstanceLocation[0]
				if _, ok := properties[name]; ok {
					pointer = "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
				}
			}
		}
		return nil, refuse("schema refused value", pointer)
	}
	return value, nil
}

// Route validates the payload, fills escaped path segments and declared query
// parameters, and puts only the remaining fields into the JSON body.
func (d Declaration) Route(baseURL string, data []byte) (string, []byte, error) {
	if len(data) > MaxInputBytes {
		return "", nil, refuse("input exceeds bound", "/")
	}
	admitted, err := Admit(d)
	if err != nil || d.Digest == "" {
		return "", nil, refuse("declaration not admitted", "/digest")
	}
	d = admitted
	input, err := validate(data, d.InputSchema, true)
	if err != nil {
		return "", nil, err
	}
	path := d.Path
	filled := map[string]bool{}
	for _, p := range parameterPattern.FindAllStringSubmatch(path, -1) {
		if filled[p[1]] {
			continue
		}
		filled[p[1]] = true
		val, ok := input[p[1]]
		if !ok {
			return "", nil, refuse("path parameter required", "/"+p[1])
		}
		str, err := scalar(val)
		if err != nil || str == "" || str == "." || str == ".." {
			return "", nil, refuse("invalid path parameter", "/"+p[1])
		}
		path = strings.ReplaceAll(path, p[0], url.PathEscape(str))
		delete(input, p[1])
	}
	query := url.Values{}
	for _, k := range d.Query {
		if val, ok := input[k]; ok {
			str, err := scalar(val)
			if err != nil {
				return "", nil, refuse("query value must be scalar", "/"+k)
			}
			query.Set(k, str)
			delete(input, k)
		}
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") {
		return "", nil, refuse("invalid source origin", "/")
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", nil, refuse("invalid route", "/")
	}
	target := base.ResolveReference(ref)
	target.RawQuery = query.Encode()
	target.Fragment = ""
	if d.Method == "GET" || d.Method == "DELETE" {
		if len(input) != 0 {
			return "", nil, refuse("method does not accept body fields", "/")
		}
		return target.String(), nil, nil
	}
	body, _ := json.Marshal(input)
	return target.String(), body, nil
}
func scalar(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		return fmt.Sprint(v), nil
	}
	return "", refuse("scalar required", "/")
}
func (d Declaration) ValidateOutput(data []byte) error {
	if len(data) > d.MaxOutputBytes {
		return refuse("output exceeds bound", "/")
	}
	_, err := validate(data, d.OutputSchema, false)
	return err
}
