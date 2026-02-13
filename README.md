# dispatch

[![Go Reference](https://pkg.go.dev/badge/github.com/bjaus/dispatch.svg)](https://pkg.go.dev/github.com/bjaus/dispatch)
[![Go Report Card](https://goreportcard.com/badge/github.com/bjaus/dispatch)](https://goreportcard.com/report/github.com/bjaus/dispatch)
[![CI](https://github.com/bjaus/dispatch/actions/workflows/ci.yml/badge.svg)](https://github.com/bjaus/dispatch/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/bjaus/dispatch/branch/main/graph/badge.svg)](https://codecov.io/gh/bjaus/dispatch)

A flexible message routing framework for event-driven Go applications.

## Features

- **Multi-Source Routing** — Route messages from webhooks, message queues, or custom formats through a single processor
- **Discriminator Pattern** — Cheap detection before expensive parsing for O(1) hot-path matching
- **Typed Handlers** — Automatic JSON unmarshaling and validation with generics
- **Proc/Func Pattern** — Fire-and-forget procedures or request-response functions
- **Replier Interface** — Built-in support for request-response transports (Step Functions, etc.)
- **Pluggable Hooks** — Observability without coupling to specific logging or metrics systems
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

// Define a procedure (fire-and-forget)
type UserCreatedProc struct{}

func (p *UserCreatedProc) Run(ctx context.Context, payload UserCreatedPayload) error {
    log.Printf("User created: %s (%s)", payload.UserID, payload.Email)
    return nil
}

// Define a source to parse your message format
type mySource struct{}

func (s *mySource) Name() string { return "my-source" }

func (s *mySource) Discriminator() dispatch.Discriminator {
    return dispatch.HasFields("type", "payload")
}

func (s *mySource) Parse(raw []byte) (dispatch.Message, error) {
    var env struct {
        Type    string          `json:"type"`
        Payload json.RawMessage `json:"payload"`
    }
    if err := json.Unmarshal(raw, &env); err != nil {
        return dispatch.Message{}, err
    }
    return dispatch.Message{Key: env.Type, Payload: env.Payload}, nil
}

func main() {
    // Create router
    r := dispatch.New()

    // Add source
    r.AddSource(&mySource{})

    // Register procedure
    dispatch.RegisterProc(r, "user/created", &UserCreatedProc{})

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
| **Handlers** | Pure business logic with typed payloads (Proc or Func) |

### Proc vs Func

The package provides two handler patterns:

```go
// Proc: Fire-and-forget (returns only error)
type Proc[T any] interface {
    Run(ctx context.Context, payload T) error
}

// Func: Request-response (returns result and error)
type Func[T, R any] interface {
    Call(ctx context.Context, payload T) (R, error)
}
```

Use `Proc` for event handlers where you don't need to send a response. Use `Func` for request-response patterns like Step Functions tasks.

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
r.AddSource(webhookSource)
r.AddSource(apiSource)

// Custom group for protobuf messages
r.AddGroup(protoInspector, grpcSource, kafkaSource)
```

## Handler Registration

```go
// Register a procedure (fire-and-forget)
dispatch.RegisterProc(r, "user/created", &UserCreatedProc{})

// Register a function (request-response)
dispatch.RegisterFunc(r, "lookup-user", &LookupUserFunc{})

// Or use function adapters for simple cases
dispatch.RegisterProcFunc(r, "ping", func(ctx context.Context, p PingPayload) error {
    return nil
})

dispatch.RegisterFuncFunc(r, "echo", func(ctx context.Context, in Input) (*Output, error) {
    return &Output{Value: in.Value}, nil
})
```

## Replier Interface

For transports that require sending responses back (like Step Functions), sources can provide a Replier:

```go
type Replier interface {
    Reply(ctx context.Context, result json.RawMessage) error
    Fail(ctx context.Context, err error) error
}
```

Example Step Functions source:

```go
type sfnReplier struct {
    sfn   SFNClient
    token string
}

func (r *sfnReplier) Reply(ctx context.Context, result json.RawMessage) error {
    return r.sfn.SendTaskSuccess(ctx, r.token, result)
}

func (r *sfnReplier) Fail(ctx context.Context, err error) error {
    return r.sfn.SendTaskFailure(ctx, r.token, err)
}

func (s *sfnSource) Parse(raw []byte) (dispatch.Message, error) {
    // ... parse envelope ...
    return dispatch.Message{
        Key:     taskType,
        Payload: payload,
        Replier: &sfnReplier{sfn: s.sfn, token: token},
    }, nil
}
```

When a Replier is present:
- On success: router calls `Replier.Reply` with the marshaled result (or `{}` for Procs)
- On error: router calls `Replier.Fail` with the error

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
        slog.InfoContext(ctx, "parsing message", "source", source, "key", key)
        return ctx
    }),
    dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
        slog.InfoContext(ctx, "handler succeeded", "source", source, "key", key, "duration", d)
    }),
    dispatch.WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
        slog.ErrorContext(ctx, "handler failed", "source", source, "key", key, "error", err, "duration", d)
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

## Validation

Payloads implementing `Validate() error` are automatically validated:

```go
type UserPayload struct {
    UserID string `json:"user_id"`
    Email  string `json:"email"`
}

func (p *UserPayload) Validate() error {
    if p.UserID == "" {
        return errors.New("user_id is required")
    }
    if p.Email == "" {
        return errors.New("email is required")
    }
    return nil
}
```

Works with any validation library (ozzo-validation, go-playground/validator, etc.) as long as your payload has a `Validate() error` method.

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

### HTTP Webhook Handler

```go
func webhookHandler(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    if err := router.Process(r.Context(), body); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    w.WriteHeader(http.StatusOK)
}
```

### Message Queue Consumer

```go
func consume(ctx context.Context, queue MessageQueue) error {
    for {
        msg, err := queue.Receive(ctx)
        if err != nil {
            return err
        }
        if err := router.Process(ctx, msg.Body); err != nil {
            msg.Nack() // retry later
            continue
        }
        msg.Ack()
    }
}
```

### Kafka Consumer

```go
func (c *Consumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
    for msg := range claim.Messages() {
        if err := c.router.Process(session.Context(), msg.Value); err != nil {
            slog.Error("processing failed", "error", err, "topic", msg.Topic)
            continue
        }
        session.MarkMessage(msg, "")
    }
    return nil
}
```

## Testing

```bash
go test -v ./...
```

## License

MIT License - see [LICENSE](LICENSE) for details.
