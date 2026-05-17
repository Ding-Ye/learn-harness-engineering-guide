package main

import "fmt"

// EchoTool is the smallest useful tool: it returns "echo: <text>".
// Its job is to prove the dispatch round-trip works without any I/O.
type EchoTool struct{}

func (EchoTool) Name() string { return "echo" }

func (EchoTool) Run(args map[string]any) (string, error) {
	text, ok := args["text"].(string)
	if !ok {
		return "", fmt.Errorf(`args.text must be a string, got %T`, args["text"])
	}
	return "echo: " + text, nil
}
