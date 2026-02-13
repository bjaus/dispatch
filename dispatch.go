package dispatch

import (
	"context"
	"encoding/json"
)

// Proc (procedure) processes a message without returning a result.
// Use this for fire-and-forget patterns like event handlers.
//
// The type parameter T is the payload type. The router automatically
// unmarshals JSON to T and validates it if T implements Validate() error.
//
// Example:
//
//	type UserCreatedProc struct {
//	    db *sql.DB
//	}
//
//	func (p *UserCreatedProc) Run(ctx context.Context, payload UserCreatedPayload) error {
//	    _, err := p.db.ExecContext(ctx, "INSERT INTO users ...", payload.UserID)
//	    return err
//	}
type Proc[T any] interface {
	Run(ctx context.Context, payload T) error
}

// ProcFunc is a function adapter for Proc. Use for simple procedures
// that don't need a struct:
//
//	dispatch.RegisterProc(r, "user/created", func(ctx context.Context, p Payload) error {
//	    return nil
//	})
type ProcFunc[T any] func(ctx context.Context, payload T) error

// Run implements the Proc interface.
func (f ProcFunc[T]) Run(ctx context.Context, payload T) error {
	return f(ctx, payload)
}

// Func (function) processes a message and returns a typed result.
// Use this for request-response patterns like Step Functions tasks.
//
// The type parameters are: T for input payload, R for result.
// The router automatically unmarshals T, validates it, and marshals R.
//
// Example:
//
//	type LookupUserFunc struct {
//	    client IdentityClient
//	}
//
//	func (f *LookupUserFunc) Call(ctx context.Context, in LookupInput) (*LookupResult, error) {
//	    user, err := f.client.GetUser(ctx, in.UserID)
//	    if err != nil {
//	        return nil, err
//	    }
//	    return &LookupResult{Email: user.Email}, nil
//	}
type Func[T, R any] interface {
	Call(ctx context.Context, payload T) (R, error)
}

// FuncFunc is a function adapter for Func. Use for simple functions
// that don't need a struct:
//
//	dispatch.RegisterFunc(r, "lookup-user", func(ctx context.Context, in Input) (*Result, error) {
//	    return &Result{...}, nil
//	})
type FuncFunc[T, R any] func(ctx context.Context, payload T) (R, error)

// Call implements the Func interface.
func (f FuncFunc[T, R]) Call(ctx context.Context, payload T) (R, error) {
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
//   - SQS messages
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
//	func (s *mySource) Parse(raw []byte) (dispatch.Message, error) {
//	    var env struct {
//	        Type    string          `json:"type"`
//	        Payload json.RawMessage `json:"payload"`
//	    }
//	    if err := json.Unmarshal(raw, &env); err != nil {
//	        return dispatch.Message{}, err
//	    }
//	    return dispatch.Message{Key: env.Type, Payload: env.Payload}, nil
//	}
type Source interface {
	// Name returns the source identifier for logging and metrics.
	Name() string

	// Discriminator returns a predicate for cheap message detection.
	// The router calls this before Parse to avoid expensive parsing
	// when the message format doesn't match.
	Discriminator() Discriminator

	// Parse attempts to parse raw bytes as this source's format.
	// Returns the parsed message and nil if successful, or an error
	// describing why parsing failed.
	Parse(raw []byte) (Message, error)
}

// SourceFunc creates a Source from a name, discriminator, and parse function.
// Use for simple sources that don't need a struct:
//
//	r.AddSource(dispatch.SourceFunc(
//	    "legacy",
//	    dispatch.HasFields("type", "payload"),
//	    func(raw []byte) (dispatch.Message, error) {
//	        // parse logic
//	    },
//	))
func SourceFunc(name string, disc Discriminator, parse func([]byte) (Message, error)) Source {
	return &sourceFunc{name: name, disc: disc, parse: parse}
}

type sourceFunc struct {
	name  string
	disc  Discriminator
	parse func([]byte) (Message, error)
}

func (s *sourceFunc) Name() string                      { return s.name }
func (s *sourceFunc) Discriminator() Discriminator      { return s.disc }
func (s *sourceFunc) Parse(raw []byte) (Message, error) { return s.parse(raw) }

// Message contains the result of source parsing.
type Message struct {
	// Key is the routing key used to find the handler.
	// This is matched against keys passed to RegisterProc/RegisterFunc.
	Key string

	// Version is the schema version of the payload, if available.
	// Sources should populate this for version-aware routing.
	Version string

	// Payload is the raw JSON to unmarshal into the handler's type.
	Payload json.RawMessage

	// Replier handles sending responses back to the caller.
	// For fire-and-forget sources (EventBridge, SNS), this is nil.
	// For request-response sources (Step Functions), this sends results back.
	//
	// When Replier is set and a Func is registered:
	//   - On success: router marshals result and calls Replier.Reply
	//   - On error: router calls Replier.Fail
	//
	// When Replier is set and a Proc is registered:
	//   - On success: router calls Replier.Reply with empty JSON ({})
	//   - On error: router calls Replier.Fail
	Replier Replier
}

// Replier sends responses back to the message originator.
// Implement this for request-response transport patterns.
type Replier interface {
	// Reply sends a successful response with the given JSON payload.
	Reply(ctx context.Context, result json.RawMessage) error

	// Fail sends a failure response with the given error.
	Fail(ctx context.Context, err error) error
}
