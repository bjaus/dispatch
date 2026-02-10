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
// # Discriminator Pattern
//
// Sources implement a two-phase matching strategy for efficient routing:
//
//  1. Discriminator: Cheap field presence/value checks
//  2. Parse: Full envelope parsing only after discriminator matches
//
// This avoids expensive JSON parsing when messages don't match a source,
// and enables O(1) hot-path matching via adaptive ordering (the last successful
// source is tried first on subsequent messages).
//
//	func (s *mySource) Discriminator() dispatch.Discriminator {
//	    return dispatch.And(
//	        dispatch.HasFields("source", "detail-type"),
//	        dispatch.FieldEquals("source", "my.service"),
//	    )
//	}
//
// Composable discriminators are provided:
//   - HasFields: Check for field presence
//   - FieldEquals: Check field value
//   - And: All discriminators must match
//   - Or: Any discriminator must match
//
// # Inspector and View
//
// The Inspector/View abstraction enables format-agnostic field access:
//
//	type Inspector interface {
//	    Inspect(raw []byte) (View, error)
//	}
//
//	type View interface {
//	    HasField(path string) bool
//	    GetString(path string) (string, bool)
//	    GetBytes(path string) ([]byte, bool)
//	}
//
// By default, the router uses JSONInspector for all sources. For mixed formats
// (e.g., JSON and protobuf), use AddGroup with a custom inspector:
//
//	r := dispatch.New()
//	r.AddSource(jsonSource)                          // Uses default JSON inspector
//	r.AddGroup(protoInspector, grpcSource, kafkaSource) // Custom inspector
//
// # Sources
//
// A Source parses raw message bytes and returns routing information:
//
//	type Source interface {
//	    Name() string
//	    Discriminator() Discriminator
//	    Parse(raw []byte) (Parsed, error)
//	}
//
// Sources are matched using their Discriminator, then parsed in registration order.
// The first source whose discriminator matches and Parse succeeds handles the message.
//
// The Parsed struct contains:
//   - Key: routing key to match against registered handlers
//   - Version: optional schema version for version-aware routing
//   - Payload: raw JSON to unmarshal into the handler's type
//   - Complete: optional callback for completion semantics (e.g., Step Functions)
//
// Example source implementation:
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
//	    return dispatch.Parsed{
//	        Key:     env.Type,
//	        Payload: env.Payload,
//	    }, nil
//	}
//
// Use SourceFunc for simple sources without a struct:
//
//	r.AddSource(dispatch.SourceFunc("custom", dispatch.HasFields("event"), parseFunc))
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
//   - WithOnParseError: Called when source's Parse returns an error
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
// AddSource, AddGroup, or Register after calling Process.
package dispatch
