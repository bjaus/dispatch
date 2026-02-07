// Package dispatch provides a flexible message routing framework for event-driven systems.
//
// The dispatch package routes messages from multiple sources (EventBridge, SNS, Step Functions,
// Kinesis, or custom formats) to typed handlers. It handles envelope parsing, payload
// unmarshaling, validation, and completion semantics — letting you focus on business logic.
//
// # Quick Start
//
// Define a handler for your event type:
//
//	type UserCreatedHandler struct {
//	    onboarding Onboarding
//	}
//
//	type UserCreatedPayload struct {
//	    UserID string `json:"user_id"`
//	    Email  string `json:"email"`
//	}
//
//	func (h *UserCreatedHandler) Handle(ctx context.Context, p UserCreatedPayload) error {
//	    return h.onboarding.RegisterUser(ctx, p.UserID, p.Email)
//	}
//
// Create a router, add sources, and register handlers:
//
//	r := dispatch.New()
//
//	r.AddSource(myEventBridgeSource)
//
//	dispatch.Register(r, "user/created", &UserCreatedHandler{onboarding})
//
//	// Process messages
//	err := r.Process(ctx, rawMessageBytes)
//
// # Design Philosophy
//
// The package separates concerns into three layers:
//
//   - Sources: Parse raw bytes and extract routing keys + payloads
//   - Router: Matches keys to handlers, orchestrates the dispatch flow
//   - Handlers: Pure business logic with typed payloads
//
// This separation allows:
//   - Multiple message formats on a single queue
//   - Transport-agnostic handler code
//   - Consistent observability via hooks
//   - Easy testing with mock sources
//
// # Sources
//
// A Source parses raw message bytes and returns routing information:
//
//	type Source interface {
//	    Name() string
//	    Parse(raw []byte) (Parsed, bool)
//	}
//
// Sources are tried in registration order. The first source that returns true
// from Parse handles the message. This allows a single queue to receive messages
// from EventBridge, SNS, Step Functions, and custom formats.
//
// The Parsed struct contains:
//   - Key: routing key to match against registered handlers
//   - Payload: raw JSON to unmarshal into the handler's type
//   - Complete: optional callback for completion semantics (e.g., Step Functions)
//
// Example source implementation:
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
//	    return dispatch.Parsed{
//	        Key:     env.Type,
//	        Payload: env.Payload,
//	    }, true
//	}
//
// # Handlers
//
// Handlers implement the Handler interface with a typed payload:
//
//	type Handler[T any] interface {
//	    Handle(ctx context.Context, payload T) error
//	}
//
// The router automatically:
//   - Unmarshals the JSON payload to the handler's type
//   - Validates the payload if it implements validation.Validatable
//   - Calls the handler with the typed payload
//
// Use HandlerFunc for simple cases without a struct:
//
//	dispatch.RegisterFunc(r, "ping", func(ctx context.Context, p PingPayload) error {
//	    return nil
//	})
//
// # Hooks
//
// Hooks provide observability without coupling to specific logging or metrics systems.
// Use functional options to configure hooks:
//
//	r := dispatch.New(
//	    dispatch.WithOnParse(func(ctx context.Context, source, key string) context.Context {
//	        return logx.WithCtx(ctx, slog.String("source", source), slog.String("key", key))
//	    }),
//	    dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
//	        metrics.Timing("dispatch.success", d, "source:"+source)
//	    }),
//	    dispatch.WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
//	        metrics.Incr("dispatch.error", "source:"+source)
//	    }),
//	)
//
// Available hooks:
//   - WithOnParse: Called after parsing, enriches context
//   - WithOnDispatch: Called just before handler executes
//   - WithOnSuccess: Called after handler succeeds
//   - WithOnFailure: Called after handler fails
//   - WithOnNoSource: Called when no source matches
//   - WithOnNoHandler: Called when no handler is registered
//   - WithOnUnmarshalError: Called on JSON unmarshal errors
//   - WithOnValidationError: Called on validation errors
//
// Multiple hooks of the same type are called in order.
//
// # Source-Specific Hooks
//
// Sources can implement optional hook interfaces to add source-specific behavior.
// These hooks run after global hooks, and both are always called:
//
//	type OnParseHook interface {
//	    OnParse(ctx context.Context, key string) context.Context
//	}
//
//	type OnSuccessHook interface {
//	    OnSuccess(ctx context.Context, key string, duration time.Duration)
//	}
//
//	type OnFailureHook interface {
//	    OnFailure(ctx context.Context, key string, err error, duration time.Duration)
//	}
//
// For error-returning hooks, if either global or source returns an error, that
// error is returned. This allows sources to override global skip/fail policies.
//
// # Composing Hooks
//
// Pass multiple hooks to New to compose them:
//
//	r := dispatch.New(
//	    // Logging hooks
//	    dispatch.WithOnParse(addLoggingContext),
//	    dispatch.WithOnSuccess(logSuccess),
//	    dispatch.WithOnFailure(logFailure),
//
//	    // Metrics hooks
//	    dispatch.WithOnSuccess(recordSuccessMetric),
//	    dispatch.WithOnFailure(recordFailureMetric),
//
//	    // Error handling hooks
//	    dispatch.WithOnNoHandler(skipUnknownEvents),
//	)
//
// Hooks are called in registration order. For context-returning hooks, the context
// chains through each hook. For error-returning hooks, the first error wins.
//
// # Completion Callbacks
//
// Sources can provide a Complete callback in Parsed for transport-specific
// completion semantics. For example, Step Functions requires SendTaskSuccess
// or SendTaskFailure after processing:
//
//	func (s *sfnSource) Parse(raw []byte) (dispatch.Parsed, bool) {
//	    // ... parse envelope ...
//	    return dispatch.Parsed{
//	        Key:     taskType,
//	        Payload: payload,
//	        Complete: func(ctx context.Context, err error) error {
//	            if err != nil {
//	                return s.sfn.SendTaskFailure(...)
//	            }
//	            return s.sfn.SendTaskSuccess(...)
//	        },
//	    }, true
//	}
//
// # Validation
//
// Payloads that implement validation.Validatable are automatically validated
// after unmarshaling:
//
//	type UserPayload struct {
//	    UserID string `json:"user_id"`
//	    Email  string `json:"email"`
//	}
//
//	func (p *UserPayload) Validate() error {
//	    return validation.ValidateStruct(p,
//	        validation.Field(&p.UserID, validation.Required),
//	        validation.Field(&p.Email, validation.Required, is.Email),
//	    )
//	}
//
// Validation errors trigger the OnValidationError hook.
//
// # Error Handling
//
// The OnNoSource, OnNoHandler, OnUnmarshalError, and OnValidationError hooks
// control what happens when errors occur:
//
//   - Return nil to skip the message (it goes to DLQ if configured)
//   - Return an error to fail (message retries based on queue configuration)
//
// By default, all errors cause failures. Override with hooks to skip bad messages:
//
//	r := dispatch.New(
//	    dispatch.WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
//	        logger.Error("bad payload", "error", err)
//	        return nil // skip to DLQ, don't retry
//	    }),
//	)
//
// # Thread Safety
//
// Router is safe for concurrent use after configuration is complete. Do not call
// AddSource or Register after calling Process.
package dispatch
