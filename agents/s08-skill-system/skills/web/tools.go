// Package web bundles the stub http_get / http_post tools shipped with the
// web skill fixture. As with the other fixtures, the implementations are
// mocks — no real HTTP calls happen, so tests stay hermetic.
package web

import (
	"context"
	"encoding/json"
	"fmt"
)

var httpGetSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "Absolute https:// URL to fetch."
    }
  },
  "required": ["url"]
}`)

// GetTool is the stub HTTP GET. Returns a canned body string that includes
// the requested URL — that way the test can verify the argument actually
// reached the handler.
type GetTool struct{}

func (GetTool) Name() string             { return "http_get" }
func (GetTool) Description() string      { return "GET an HTTP(S) URL and return the body." }
func (GetTool) Schema() json.RawMessage  { return httpGetSchema }

func (GetTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode args: %w", err)
	}
	if input.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	return fmt.Sprintf("(stub) HTTP 200 body for GET %s", input.URL), nil
}

var httpPostSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "Absolute https:// URL to POST to."
    },
    "body": {
      "type": "string",
      "description": "Raw request body (caller-encoded JSON or form-data)."
    }
  },
  "required": ["url", "body"]
}`)

// PostTool is the stub HTTP POST. Same canned style; the response echoes
// the body size so the model can confirm the payload reached the stub.
type PostTool struct{}

func (PostTool) Name() string             { return "http_post" }
func (PostTool) Description() string      { return "POST a body to an HTTP(S) URL and return the response." }
func (PostTool) Schema() json.RawMessage  { return httpPostSchema }

func (PostTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		URL  string `json:"url"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode args: %w", err)
	}
	if input.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	return fmt.Sprintf("(stub) HTTP 200 body for POST %s (%d bytes sent)", input.URL, len(input.Body)), nil
}
