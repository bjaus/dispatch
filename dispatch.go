package dispatch

import (
	"context"
	"encoding/json"
)

// Handler processes a typed payload. This is the primary interface to implement.
//
// The type parameter T is the payload type that the handler expects. The router
// automatically unmarshals the raw JSON payload to this type and validates it
// if T implements validation.Validatable.
//
// Example:
//
//	type UserCreatedHandler struct {
//	    db *sql.DB
//	}
//
//	func (h *UserCreatedHandler) Handle(ctx context.Context, p UserCreatedPayload) error {
//	    _, err := h.db.ExecContext(ctx, "INSERT INTO users ...", p.UserID, p.Email)
//	    return err
//	}
type Handler[T any] interface {
	Handle(ctx context.Context, payload T) error
}

// HandlerFunc is a function adapter for Handler. Use this for simple handlers
// that don't need a struct:
//
//	dispatch.RegisterFunc(r, "ping", func(ctx context.Context, p PingPayload) error {
//	    return nil
//	})
type HandlerFunc[T any] func(ctx context.Context, payload T) error

// Handle implements the Handler interface.
func (f HandlerFunc[T]) Handle(ctx context.Context, payload T) error {
	return f(ctx, payload)
}

// Source parses raw message bytes and extracts routing information.
//
// Sources are registered with Router.AddSource and tried in registration order.
// The first source whose Parse method returns true handles the message.
//
// Implement Source to support different message formats:
//   - EventBridge events
//   - SNS notifications
//   - Step Functions task tokens
//   - Kinesis records
//   - Custom formats
//
// Example:
//
//	type mySource struct{}
//
//	func (s *mySource) Name() string { return "my-source" }
//
//	func (s *mySource) Parse(raw []byte) (dispatch.Parsed, bool) {
//	    var env struct {
//	        Type    string          `json:"type"`
//	        Payload json.RawMessage `json:"payload"`
//	    }
//	    if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
//	        return dispatch.Parsed{}, false
//	    }
//	    return dispatch.Parsed{Key: env.Type, Payload: env.Payload}, true
//	}
type Source interface {
	// Name returns the source identifier for logging and metrics.
	Name() string

	// Parse attempts to parse raw bytes as this source's format.
	// Returns the parsed result and true if successful, or false if this
	// source does not recognize the message format.
	Parse(raw []byte) (Parsed, bool)
}

// SourceFunc creates a Source from a name and parse function.
// Use this for simple sources that don't need a struct:
//
//	r.AddSource(dispatch.SourceFunc("legacy", func(raw []byte) (dispatch.Parsed, bool) {
//	    // parse logic
//	}))
func SourceFunc(name string, parse func([]byte) (Parsed, bool)) Source {
	return &sourceFunc{name: name, parse: parse}
}

type sourceFunc struct {
	name  string
	parse func([]byte) (Parsed, bool)
}

func (s *sourceFunc) Name() string                    { return s.name }
func (s *sourceFunc) Parse(raw []byte) (Parsed, bool) { return s.parse(raw) }

// Parsed contains the result of source parsing.
type Parsed struct {
	// Key is the routing key used to find the handler.
	// This is matched against keys passed to Register.
	Key string

	// Payload is the raw JSON to unmarshal into the handler's type.
	Payload json.RawMessage

	// Complete is called after the handler finishes, regardless of success or failure.
	// Use this for transport-specific completion semantics like Step Functions
	// SendTaskSuccess/SendTaskFailure.
	//
	// If Complete is nil, no completion callback is made.
	// If Complete returns an error, that error is returned from Process.
	// The err parameter is the handler's error (or nil on success).
	Complete func(ctx context.Context, err error) error
}
