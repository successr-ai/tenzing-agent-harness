package tenzing_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/tenzing"
)

func TestCallSystemOne(t *testing.T) {
	var got struct {
		Model     string                      `json:"model"`
		Questions map[string]tenzing.Question `json:"questions"`
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"model":"jev-1.13.0","answers":{"destructive":{"type":"noul","noul":0.97}},"usage":{"input_tokens":5}}`)
	}))
	defer srv.Close()

	resp, err := tenzing.CallSystemOne(context.Background(), tenzing.SystemOneCall{
		APIKey:    "k",
		URL:       srv.URL + "/api/alpha/decisions",
		Model:     "typesafe/jev-1.13",
		State:     "rm -rf ./build",
		Questions: map[string]tenzing.Question{"destructive": tenzing.NewNoul("Does this delete files?")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/alpha/decisions" || got.Model != "typesafe/jev-1.13" || got.Questions["destructive"].Type != "noul" {
		t.Errorf("request: path=%q model=%q questions=%+v", path, got.Model, got.Questions)
	}
	if resp.Answers["destructive"].Noul != 0.97 || resp.Model != "jev-1.13.0" {
		t.Errorf("resp = %+v", resp)
	}
}

// Every connection field is required: nothing falls back to a default
// endpoint or model.
func TestCallSystemOneRequiresEveryField(t *testing.T) {
	full := tenzing.SystemOneCall{APIKey: "k", URL: "http://127.0.0.1:1", Model: "m"}
	for _, tt := range []struct {
		name, want string
		edit       func(*tenzing.SystemOneCall)
	}{
		{"no key", "API key is required", func(c *tenzing.SystemOneCall) { c.APIKey = "" }},
		{"no URL", "URL is required", func(c *tenzing.SystemOneCall) { c.URL = "" }},
		{"no model", "model is required", func(c *tenzing.SystemOneCall) { c.Model = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := full
			tt.edit(&c)
			_, err := tenzing.CallSystemOne(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}
