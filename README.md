# dispatch

[![Go Reference](https://pkg.go.dev/badge/github.com/bjaus/dispatch.svg)](https://pkg.go.dev/github.com/bjaus/dispatch)
[![Go Report Card](https://goreportcard.com/badge/github.com/bjaus/dispatch)](https://goreportcard.com/report/github.com/bjaus/dispatch)
[![CI](https://github.com/bjaus/dispatch/actions/workflows/ci.yml/badge.svg)](https://github.com/bjaus/dispatch/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/bjaus/dispatch/branch/main/graph/badge.svg)](https://codecov.io/gh/bjaus/dispatch)

A flexible message routing framework for event-driven Go applications.

## Features

- **Multi-Source Routing** — Route messages from EventBridge, SNS, Step Functions, Kinesis, or custom formats through a single queue
- **Discriminator Pattern** — Cheap detection before expensive parsing for O(1) hot-path matching
- **Typed Handlers** — Automatic JSON unmarshaling and validation with generics
- **Pluggable Hooks** — Observability without coupling to specific logging or metrics systems
- **Completion Callbacks** — Built-in support for Step Functions `SendTaskSuccess`/`SendTaskFailure`
- **Format Agnostic** — Inspector/View abstraction supports JSON, protobuf, or custom formats
- **Zero Allocation Matching** — Uses gjson for efficient JSON field lookups

## Installation

```bash
go get github.com/bjaus/dispatch
```

Requires Go 1.25 or later.

## Quick Start

```go
package main

import (
    "context"
    "encoding/json"
    "log"

    "github.com/bjaus/dispatch"
)

// Define your payload type
type UserCreatedPayload struct {
    UserID string `json:"user_id"`
    Email  string `json:"email"`
}

// Define your handler
type UserCreatedHandler struct{}

func (h *UserCreatedHandler) Handle(ctx context.Context, p UserCreatedPayload) error {
    log.Printf("User created: %s (%s)", p.UserID, p.Email)
    return nil
}

// Define a source to parse your message format
type mySource struct{}

func (s *mySource) Name() string { return "my-source" }

func (s *mySource) Discriminator() dispatch.Discriminator {
    return dispatch.HasFields("type", "payload")
}

func (s *mySource) Parse(raw []byte) (dispatch.Parsed, bool) {
    var env struct {
        Type    string          `json:"type"`
        Payload json.RawMessage `json:"payload"`
    }
    if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
        return dispatch.Parsed{}, false
    }
    return dispatch.Parsed{Key: env.Type, Payload: env.Payload}, true
}

func main() {
    // Create router
    r := dispatch.New()

    // Add source
    r.AddSource(&mySource{})

    // Register handler
    dispatch.Register(r, "user/created", &UserCreatedHandler{})

    // Process a message
    msg := []byte(`{"type": "user/created", "payload": {"user_id": "123", "email": "test@example.com"}}`)
    if err := r.Process(context.Background(), msg); err != nil {
        log.Fatal(err)
    }
}
```

## Architecture

The package separates concerns into three layers:

| Layer | Responsibility |
|-------|---------------|
| **Sources** | Parse raw bytes, extract routing key + payload |
| **Router** | Match keys to handlers, orchestrate dispatch flow |
| **Handlers** | Pure business logic with typed payloads |

### Discriminator Pattern

Sources implement a two-phase matching strategy:

1. **Discriminator** — Cheap field presence/value checks using the Inspector/View abstraction
2. **Parse** — Full envelope parsing only after discriminator matches

This avoids expensive parsing when messages don't match, and enables O(1) hot-path matching via adaptive ordering (last successful source is tried first).

```go
func (s *mySource) Discriminator() dispatch.Discriminator {
    // Cheap check: does the message have these fields?
    return dispatch.And(
        dispatch.HasFields("source", "detail-type", "detail"),
        dispatch.FieldEquals("source", "my.service"),
    )
}
```

### Inspector Groups

By default, all sources use the JSON inspector. For mixed formats (e.g., JSON + protobuf), use groups:

```go
r := dispatch.New()

// Default group uses JSON inspector
r.AddSource(eventBridgeSource)
r.AddSource(snsSource)

// Custom group for protobuf messages
r.AddGroup(protoInspector, grpcSource, kafkaSource)
```

## Discriminators

Composable predicates for source matching:

```go
// Check field presence
dispatch.HasFields("type", "payload")

// Check field value
dispatch.FieldEquals("source", "aws.events")

// Combine with And/Or
dispatch.And(
    dispatch.HasFields("detail-type"),
    dispatch.Or(
        dispatch.FieldEquals("source", "service.a"),
        dispatch.FieldEquals("source", "service.b"),
    ),
)
```

## Hooks

Add observability without coupling to specific systems:

```go
r := dispatch.New(
    dispatch.WithOnParse(func(ctx context.Context, source, key string) context.Context {
        return logx.WithCtx(ctx, slog.String("source", source))
    }),
    dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
        metrics.Timing("dispatch.success", d, "source:"+source)
    }),
    dispatch.WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
        metrics.Incr("dispatch.failure", "source:"+source)
    }),
)
```

### Available Hooks

| Hook | Called When |
|------|-------------|
| `WithOnParse` | After source parses message (enriches context) |
| `WithOnDispatch` | Just before handler executes |
| `WithOnSuccess` | After handler succeeds |
| `WithOnFailure` | After handler fails |
| `WithOnNoSource` | No source matches the message |
| `WithOnNoHandler` | No handler registered for key |
| `WithOnUnmarshalError` | JSON unmarshal fails |
| `WithOnValidationError` | Payload validation fails |

### Source-Specific Hooks

Sources can implement hook interfaces for source-specific behavior:

```go
type OnParseHook interface {
    OnParse(ctx context.Context, key string) context.Context
}

type OnSuccessHook interface {
    OnSuccess(ctx context.Context, key string, duration time.Duration)
}
```

## Completion Callbacks

For transports that require acknowledgment (e.g., Step Functions):

```go
func (s *sfnSource) Parse(raw []byte) (dispatch.Parsed, bool) {
    // ... parse envelope with task token ...
    return dispatch.Parsed{
        Key:     taskType,
        Payload: payload,
        Complete: func(ctx context.Context, err error) error {
            if err != nil {
                _, e := s.sfn.SendTaskFailure(ctx, &sfn.SendTaskFailureInput{
                    TaskToken: &token,
                    Error:     aws.String("HandlerError"),
                    Cause:     aws.String(err.Error()),
                })
                return e
            }
            _, e := s.sfn.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{
                TaskToken: &token,
                Output:    aws.String("{}"),
            })
            return e
        },
    }, true
}
```

## Validation

Payloads implementing `Validate() error` are automatically validated:

```go
type UserPayload struct {
    UserID string `json:"user_id"`
    Email  string `json:"email"`
}

func (p *UserPayload) Validate() error {
    return validation.ValidateStruct(p,
        validation.Field(&p.UserID, validation.Required),
        validation.Field(&p.Email, validation.Required, is.Email),
    )
}
```

## Error Handling

Error hooks control skip vs. fail behavior:

```go
r := dispatch.New(
    // Skip unknown events (go to DLQ)
    dispatch.WithOnNoHandler(func(ctx context.Context, source, key string) error {
        log.Printf("skipping unknown event: %s", key)
        return nil // nil = skip, error = fail
    }),

    // Skip malformed payloads
    dispatch.WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
        log.Printf("bad payload: %v", err)
        return nil
    }),
)
```

## Integration Patterns

### ECS Consumer with SQS Poller

```go
type consumer struct {
    router *dispatch.Router
}

func (c *consumer) ProcessMessage(ctx context.Context, msg []byte) error {
    return c.router.Process(ctx, msg)
}
```

### Lambda with SQS Events

```go
func handler(ctx context.Context, event events.SQSEvent) error {
    for _, record := range event.Records {
        if err := router.Process(ctx, []byte(record.Body)); err != nil {
            return err
        }
    }
    return nil
}
```

### Lambda with Step Functions

```go
func handler(ctx context.Context, event json.RawMessage) error {
    return router.Process(ctx, event)
}
```

## Testing

```bash
go test -v ./...
```

## License

MIT License - see [LICENSE](LICENSE) for details.
