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
// Sources are registered with Router.AddSource and matched using their
// Discriminator before Parse is called. This allows cheap detection before
// expensive parsing.
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
//	func (s *mySource) Discriminator() dispatch.Discriminator {
//	    return dispatch.HasFields("type", "payload")
//	}
//
//	func (s *mySource) Parse(raw []byte) (dispatch.Parsed, error) {
//	    var env struct {
//	        Type    string          `json:"type"`
//	        Payload json.RawMessage `json:"payload"`
//	    }
//	    if err := json.Unmarshal(raw, &env); err != nil {
//	        return dispatch.Parsed{}, err
//	    }
//	    if env.Type == "" {
//	        return dispatch.Parsed{}, errors.New("missing type field")
//	    }
//	    return dispatch.Parsed{Key: env.Type, Payload: env.Payload}, nil
//	}
type Source interface {
	// Name returns the source identifier for logging and metrics.
	Name() string

	// Discriminator returns a predicate for cheap message detection.
	// The router calls this before Parse to avoid expensive parsing
	// when the message format doesn't match.
	Discriminator() Discriminator

	// Parse attempts to parse raw bytes as this source's format.
	// Returns the parsed result and nil if successful, or an error describing
	// why parsing failed.
	Parse(raw []byte) (Parsed, error)
}

// SourceFunc creates a Source from a name, discriminator, and parse function.
// Use this for simple sources that don't need a struct:
//
//	r.AddSource(dispatch.SourceFunc(
//	    "legacy",
//	    dispatch.HasFields("type", "payload"),
//	    func(raw []byte) (dispatch.Parsed, error) {
//	        // parse logic
//	    },
//	))
func SourceFunc(name string, disc Discriminator, parse func([]byte) (Parsed, error)) Source {
	return &sourceFunc{name: name, disc: disc, parse: parse}
}

type sourceFunc struct {
	name  string
	disc  Discriminator
	parse func([]byte) (Parsed, error)
}

func (s *sourceFunc) Name() string                     { return s.name }
func (s *sourceFunc) Discriminator() Discriminator     { return s.disc }
func (s *sourceFunc) Parse(raw []byte) (Parsed, error) { return s.parse(raw) }

// Parsed contains the result of source parsing.
type Parsed struct {
	// Key is the routing key used to find the handler.
	// This is matched against keys passed to Register.
	Key string

	// Version is the schema version of the payload, if available.
	// Sources should populate this for version-aware routing.
	Version string

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
