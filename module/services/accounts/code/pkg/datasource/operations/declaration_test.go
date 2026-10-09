package operations

import (
	"encoding/json"
	"strings"
	"testing"
)

func declaration(t *testing.T) Declaration {
	t.Helper()
	d, err := Admit(Declaration{Name: "read_item", Method: "GET", Path: "/items/{id}", Query: []string{"filter"}, Effect: ReadOnly, MaxOutputBytes: 1024,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string"},"filter":{"type":"string"}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestDeclarationRouteAndDigest(t *testing.T) {
	d := declaration(t)
	target, body, err := d.Route("https://api.example.com", []byte(`{"id":"a/b?c","filter":"x&z"}`))
	if err != nil || target != "https://api.example.com/items/a%2Fb%3Fc?filter=x%26z" || len(body) != 0 {
		t.Fatalf("route=%s body=%s err=%v", target, body, err)
	}
	changed := d
	changed.Path = "/other/{id}"
	if _, err := Admit(changed); err == nil {
		t.Fatal("route changed under same digest")
	}
	original := d
	original.Digest = ""
	original.InputSchema = json.RawMessage(`{"properties":{"filter":{"type":"string"},"id":{"type":"string"}},"additionalProperties":false,"type":"object"}`)
	again, err := Admit(original)
	if err != nil || again.Digest != d.Digest {
		t.Fatal("canonical JSON changed digest")
	}
}
func TestInputAndOutputRefusals(t *testing.T) {
	d := declaration(t)
	for _, input := range []string{`{}`, `{"id":false}`, `{"id":"one","destination":"https://example.com"}`, `{"id":"one"}garbage`, `{"id":".."}`} {
		if _, _, err := d.Route("https://api.example.com", []byte(input)); err == nil {
			t.Errorf("admitted %s", input)
		}
	}
	if _, _, err := d.Route("https://api.example.com", []byte(`{"id":false}`)); err == nil || !strings.Contains(err.Error(), "/id") {
		t.Fatalf("missing schema pointer: %v", err)
	}
	for _, output := range []string{`{"ok":"wrong"}`, `[]`, `{"ok":true,"extra":1}`, strings.Repeat(" ", 1025)} {
		if err := d.ValidateOutput([]byte(output)); err == nil {
			t.Error("admitted invalid output")
		}
	}
	if err := d.ValidateOutput([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
}
func TestDeclarationAdmission(t *testing.T) {
	cases := map[string]func(*Declaration){
		"origin":        func(d *Declaration) { d.Path = "//example.com/" },
		"traversal":     func(d *Declaration) { d.Path = "/../items" },
		"encoding":      func(d *Declaration) { d.Path = "/%2e%2e/items" },
		"query":         func(d *Declaration) { d.Path = "/items?api_key=bad" },
		"unknown_param": func(d *Declaration) { d.Path = "/items/{missing}" },
		"open_input":    func(d *Declaration) { d.InputSchema = json.RawMessage(`{"type":"object"}`) },
		"remote_schema": func(d *Declaration) {
			d.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"$ref":"https://example.com/schema"}`)
		},
		"effect": func(d *Declaration) { d.Effect = "" },
		"bound":  func(d *Declaration) { d.MaxOutputBytes = 0 },
		"method": func(d *Declaration) { d.Method = "CONNECT" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := declaration(t)
			d.Digest = ""
			change(&d)
			if _, err := Admit(d); err == nil {
				t.Fatal("admitted invalid declaration")
			}
		})
	}
}
func TestBodyMapping(t *testing.T) {
	d := declaration(t)
	d.Digest = ""
	d.Query = nil
	d, err := Admit(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Route("https://api.example.com", []byte(`{"id":"1","filter":"x"}`)); err == nil {
		t.Fatal("GET body was admitted")
	}
	d.Digest = ""
	d.Method = "POST"
	d.Effect = Mutation
	d, err = Admit(d)
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := d.Route("https://api.example.com", []byte(`{"id":"1","filter":"x"}`))
	if err != nil || string(body) != `{"filter":"x"}` {
		t.Fatalf("body mapping %s: %v", body, err)
	}
}
