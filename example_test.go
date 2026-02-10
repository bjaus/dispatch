package dispatch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/bjaus/dispatch"
)

// UserCreatedPayload is the payload for user/created events.
type UserCreatedPayload struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// UserCreatedHandler handles user/created events.
type UserCreatedHandler struct{}

func (h *UserCreatedHandler) Handle(ctx context.Context, p UserCreatedPayload) error {
	fmt.Printf("User created: %s (%s)\n", p.UserID, p.Email)
	return nil
}

// simpleSource is a minimal source implementation for examples.
type simpleSource struct{}

func (s *simpleSource) Name() string { return "simple" }

func (s *simpleSource) Discriminator() dispatch.Discriminator {
	return dispatch.HasFields("type", "payload")
}

func (s *simpleSource) Parse(raw []byte) (dispatch.Parsed, error) {
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return dispatch.Parsed{}, err
	}
	if env.Type == "" {
		return dispatch.Parsed{}, fmt.Errorf("missing type field")
	}
	return dispatch.Parsed{
		Key:     env.Type,
		Payload: env.Payload,
	}, nil
}

func Example() {
	// Create router with hooks
	r := dispatch.New(
		dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
			log.Printf("[%s] %s succeeded (%v)", source, key, d)
		}),
		dispatch.WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
			log.Printf("[%s] %s failed: %v (%v)", source, key, err, d)
		}),
	)

	// Add source
	r.AddSource(&simpleSource{})

	// Register handler
	dispatch.Register(r, "user/created", &UserCreatedHandler{})

	// Process a message
	msg := []byte(`{"type": "user/created", "payload": {"user_id": "123", "email": "test@example.com"}}`)
	if err := r.Process(context.Background(), msg); err != nil {
		log.Fatal(err)
	}

	// Output:
	// User created: 123 (test@example.com)
}

func Example_handlerFunc() {
	r := dispatch.New()
	r.AddSource(&simpleSource{})

	// Register with a function instead of a struct
	dispatch.RegisterFunc(r, "ping", func(ctx context.Context, p struct{ Message string }) error {
		fmt.Println("Ping:", p.Message)
		return nil
	})

	msg := []byte(`{"type": "ping", "payload": {"message": "hello"}}`)
	_ = r.Process(context.Background(), msg)

	// Output:
	// Ping: hello
}

func Example_sourceFunc() {
	r := dispatch.New()

	// Use SourceFunc for simple sources
	r.AddSource(dispatch.SourceFunc("custom", dispatch.HasFields("event", "data"), func(raw []byte) (dispatch.Parsed, error) {
		var env struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return dispatch.Parsed{}, err
		}
		if env.Event == "" {
			return dispatch.Parsed{}, fmt.Errorf("missing event field")
		}
		return dispatch.Parsed{Key: env.Event, Payload: env.Data}, nil
	}))

	dispatch.RegisterFunc(r, "hello", func(ctx context.Context, p struct{ Name string }) error {
		fmt.Println("Hello,", p.Name)
		return nil
	})

	msg := []byte(`{"event": "hello", "data": {"name": "World"}}`)
	_ = r.Process(context.Background(), msg)

	// Output:
	// Hello, World
}

func Example_multipleHooks() {
	// Pass multiple hooks to New
	r := dispatch.New(
		dispatch.WithOnDispatch(func(ctx context.Context, source, key string) {
			fmt.Printf("Processing %s from %s\n", key, source)
		}),
		dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
			fmt.Printf("Metric: %s.%s.success\n", source, key)
		}),
	)
	r.AddSource(&simpleSource{})

	dispatch.RegisterFunc(r, "test", func(ctx context.Context, p struct{}) error {
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {}}`)
	_ = r.Process(context.Background(), msg)

	// Output:
	// Processing test from simple
	// Metric: simple.test.success
}

func Example_skipBadMessages() {
	r := dispatch.New(
		dispatch.WithOnNoHandler(func(ctx context.Context, source, key string) error {
			fmt.Println("Skipping unknown event:", key)
			return nil // return nil to skip, error to fail
		}),
		dispatch.WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
			fmt.Println("Skipping bad payload:", err)
			return nil // skip bad payloads
		}),
	)

	r.AddSource(&simpleSource{})

	// No handler registered for "unknown" - will be skipped
	msg := []byte(`{"type": "unknown", "payload": {}}`)
	err := r.Process(context.Background(), msg)
	fmt.Println("Error:", err)

	// Output:
	// Skipping unknown event: unknown
	// Error: <nil>
}

// completionSource demonstrates a source with completion callback.
type completionSource struct{}

func (s *completionSource) Name() string { return "completion" }

func (s *completionSource) Discriminator() dispatch.Discriminator {
	return dispatch.HasFields("task", "token", "payload")
}

func (s *completionSource) Parse(raw []byte) (dispatch.Parsed, error) {
	var env struct {
		Task    string          `json:"task"`
		Token   string          `json:"token"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return dispatch.Parsed{}, err
	}
	if env.Token == "" {
		return dispatch.Parsed{}, fmt.Errorf("missing token field")
	}
	return dispatch.Parsed{
		Key:     env.Task,
		Payload: env.Payload,
		Complete: func(ctx context.Context, err error) error {
			if err != nil {
				fmt.Printf("Task %s failed: %v\n", env.Token, err)
			} else {
				fmt.Printf("Task %s succeeded\n", env.Token)
			}
			return nil
		},
	}, nil
}

func Example_completion() {
	r := dispatch.New()
	r.AddSource(&completionSource{})

	dispatch.RegisterFunc(r, "process", func(ctx context.Context, p struct{ Value int }) error {
		fmt.Println("Processing value:", p.Value)
		return nil
	})

	msg := []byte(`{"task": "process", "token": "abc123", "payload": {"value": 42}}`)
	_ = r.Process(context.Background(), msg)

	// Output:
	// Processing value: 42
	// Task abc123 succeeded
}
