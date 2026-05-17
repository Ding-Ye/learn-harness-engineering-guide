package main

import (
	"context"
	"fmt"
	"log"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s01-minimum-loop <user message>")
		fmt.Fprintln(os.Stderr, "  (runs the MockProvider so any prompt is accepted; output is scripted)")
		os.Exit(2)
	}

	// A scripted conversation that demonstrates one tool round-trip:
	//   1. model asks for echo with the user's text
	//   2. model emits final text after seeing the tool result
	mock := &MockProvider{
		Responses: []ChatResponse{
			{
				StopReason: "tool_use",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Name: "echo",
					Args: map[string]any{"text": os.Args[1]},
				}},
			},
			{
				Content:    fmt.Sprintf("I ran the echo tool on %q. Task complete.", os.Args[1]),
				StopReason: "end_turn",
			},
		},
	}

	loop := &Loop{
		Provider: mock,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
	}

	final, err := loop.Run(context.Background(), os.Args[1])
	if err != nil {
		log.Fatalf("loop error: %v", err)
	}
	fmt.Println(final)
}
