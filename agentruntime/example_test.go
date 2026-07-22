package agentruntime_test

import (
	"context"
	"encoding/json"
	"fmt"

	"harness-api/agentruntime"
	openaiadapter "harness-api/agentruntime/modeladapter/openai"
	"harness-api/provider"
	provideropenai "harness-api/provider/openai"
	"harness-api/storage/inmemory"
	"harness-api/toolexecution"
)

// ExampleRuntime wires the caller-owned tool channels, workers, storage, and
// runtime. It uses a local completed model stream so it is deterministic and
// never needs OpenAI credentials or a network connection.
func ExampleRuntime() {
	ctx, cancel := context.WithCancel(context.Background())

	requests := make(chan agentruntime.ToolRequest, 4)
	results := make(chan agentruntime.ToolResultEnvelope, 4)
	interrupts := make(chan agentruntime.ToolInterrupt, 4)

	registry := toolexecution.NewRegistry()
	must(registry.Register(toolexecution.Tool{
		Definition: agentruntime.ToolDefinition{
			Name:        "echo",
			Description: "Returns its input.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}))
	executor, err := toolexecution.NewExecutor(registry, 1)
	must(err)
	executorDone := make(chan error, 1)
	go func() {
		executorDone <- executor.Run(ctx, requests, results, interrupts)
	}()

	// In production, use this adapter as Config.Model. Constructing it performs
	// no I/O; this example instead uses exampleModel to keep its output local.
	openAIModel := openaiadapter.New(
		provideropenai.NewProvider(provideropenai.Config{}),
		openaiadapter.Config{Model: "gpt-4.1-mini"},
	)
	_ = openAIModel

	runtime, err := agentruntime.New(ctx, agentruntime.Config{
		Model:          exampleModel{},
		Messages:       inmemory.NewMessageStorage(),
		Tools:          registry.Definitions(),
		ToolRequests:   requests,
		ToolResults:    results,
		ToolInterrupts: interrupts,
		IDGenerator:    &exampleIDGenerator{},
	})
	must(err)

	run, subscription, err := runtime.StartSubscribed(ctx, agentruntime.Request{
		SessionID: "session_demo",
		Message: agentruntime.Message{
			Type:    agentruntime.MessageTypeUser,
			Content: "Say hello.",
		},
	})
	must(err)

	// The runtime generated this TurnID. To interrupt only this active turn,
	// call runtime.Interrupt(ctx, run.SessionID(), run.TurnID(), "cancelled").
	fmt.Printf("turn=%s\n", run.TurnID())
	for event := range subscription.Events {
		fmt.Printf("event=%s\n", event.Type)
	}
	result, err := run.Result()
	must(err)
	fmt.Printf("result=%s steps=%d\n", result.Content, result.Steps)

	cancel()
	must(<-executorDone)

	// Output:
	// turn=turn_1
	// event=run_started
	// event=provider_event_received
	// event=run_completed
	// result=hello steps=1
}

type exampleIDGenerator struct{ next int }

func (g *exampleIDGenerator) NewID(prefix string) (string, error) {
	g.next++
	return fmt.Sprintf("%s%d", prefix, g.next), nil
}

type exampleModel struct{}

func (exampleModel) Start(context.Context, agentruntime.ModelRequest) (agentruntime.ModelStream, error) {
	return exampleModelStream{}, nil
}

type exampleModelStream struct{}

func (exampleModelStream) Subscribe(context.Context) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	events <- provider.StreamEvent{
		Type: provider.StreamCompleted,
		Payload: provider.StreamCompletedPayload{Result: provider.StreamResult{
			Content:  "hello",
			Finished: true,
		}},
	}
	close(events)
	return events
}

func (exampleModelStream) Result() (provider.StreamResult, error) {
	return provider.StreamResult{Content: "hello", Finished: true}, nil
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
