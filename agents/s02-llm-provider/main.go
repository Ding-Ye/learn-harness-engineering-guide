package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

// providerProfiles bundles the boilerplate for each supported endpoint:
// base URL, default model, and the environment-variable name that holds the
// API key. Listing them in one place keeps the switch in main() tiny.
//
// "mock" is the default so `go run . "hello"` keeps working offline — that's
// the contract from s01 that downstream chapters and CI still rely on.
var providerProfiles = map[string]struct {
	BaseURL string
	Model   string
	APIKey  string // env var NAME (not the key itself)
}{
	"mock":       {Model: "mock", APIKey: ""},
	"anthropic":  {Model: "claude-sonnet-4-5", APIKey: "ANTHROPIC_API_KEY"},
	"openai":     {BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", APIKey: "OPENAI_API_KEY"},
	"deepseek":   {BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKey: "DEEPSEEK_API_KEY"},
	"moonshot":   {BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-8k", APIKey: "MOONSHOT_API_KEY"},
	"qwen":       {BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus", APIKey: "DASHSCOPE_API_KEY"},
	"groq":       {BaseURL: "https://api.groq.com/openai/v1", Model: "llama-3.3-70b-versatile", APIKey: "GROQ_API_KEY"},
	"openrouter": {BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-4o-mini", APIKey: "OPENROUTER_API_KEY"},
	"local":      {BaseURL: "http://localhost:8000/v1", Model: "local-model", APIKey: "OPENAI_API_KEY"},
}

func main() {
	providerFlag := flag.String("provider", envOr("PROVIDER", "mock"),
		"provider: mock|anthropic|openai|deepseek|moonshot|qwen|groq|openrouter|local")
	baseURL := flag.String("base-url", envOr("BASE_URL", ""), "override base URL (OpenAI-compat providers)")
	modelFlag := flag.String("model", envOr("MODEL", ""), "override model id")
	scriptPath := flag.String("script", "testdata/two_turn.json", "mock script (only when -provider=mock)")
	maxTurns := flag.Int("max-turns", 5, "max agent turns")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr,
			"usage: s02-llm-provider [-provider P] [-base-url URL] [-model ID] [-script path] [-max-turns N] <prompt>")
		os.Exit(2)
	}
	userMessage := strings.Join(flag.Args(), " ")

	prof, ok := providerProfiles[*providerFlag]
	if !ok {
		log.Fatalf("unknown -provider %q (valid: mock|anthropic|openai|deepseek|moonshot|qwen|groq|openrouter|local)", *providerFlag)
	}

	// Resolve the actual model id and base URL, with CLI overrides winning
	// over the profile defaults.
	model := *modelFlag
	if model == "" {
		model = prof.Model
	}
	url := *baseURL
	if url == "" {
		url = prof.BaseURL
	}

	var provider Provider
	switch *providerFlag {
	case "mock":
		// Stay offline: replay the scripted JSON like s01 did. This is what
		// `go run . "hello"` runs by default.
		mp, err := NewMockProviderFromFile(*scriptPath)
		if err != nil {
			log.Fatalf("load mock script: %v", err)
		}
		provider = mp
	case "anthropic":
		key := os.Getenv(prof.APIKey)
		if key == "" {
			log.Fatalf("%s is not set", prof.APIKey)
		}
		provider = NewAnthropicProvider(key)
	default:
		// Everything else is OpenAI-compatible — same translation layer, just
		// a different baseURL/model/key.
		key := os.Getenv(prof.APIKey)
		if key == "" {
			log.Fatalf("%s is not set", prof.APIKey)
		}
		provider = NewOpenAIProvider(key, url, model)
	}

	loop := &Loop{
		Provider: provider,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: *maxTurns,
		Model:    model,
		System:   "You are a helpful assistant. Use the echo tool when asked.",
	}

	final, err := loop.Run(context.Background(), userMessage)
	if err != nil {
		log.Fatalf("loop error: %v", err)
	}
	fmt.Println(final)
}

// envOr returns the env var if set & non-empty, else the fallback. Lets the
// caller drive the CLI from a shell profile or a `.env` file without having
// to remember every flag.
func envOr(k, fb string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fb
}
