package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	providerName := flag.String("provider", "mock", "provider to use: mock | anthropic")
	scriptPath := flag.String("script", "testdata/two_turn.json", "mock script (only when -provider=mock)")
	model := flag.String("model", "claude-sonnet-4-20250514", "model id (only when -provider=anthropic)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: s02-llm-provider [-provider=mock|anthropic] [-script=path] [-model=id] <user message>")
		os.Exit(2)
	}
	userMessage := args[0]

	var provider Provider
	switch *providerName {
	case "mock":
		mp, err := NewMockProviderFromFile(*scriptPath)
		if err != nil {
			log.Fatalf("load mock script: %v", err)
		}
		provider = mp
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			log.Fatalf("ANTHROPIC_API_KEY is not set")
		}
		provider = NewAnthropicProvider(key)
	default:
		log.Fatalf("unknown provider %q; expected mock or anthropic", *providerName)
	}

	loop := &Loop{
		Provider: provider,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
		Model:    *model,
		System:   "You are a helpful assistant. Use the echo tool when asked.",
	}

	final, err := loop.Run(context.Background(), userMessage)
	if err != nil {
		log.Fatalf("loop error: %v", err)
	}
	fmt.Println(final)
}
