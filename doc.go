// Package dispatch provides a flexible message routing framework for event-driven systems.
//
// The dispatch package routes messages from multiple sources (EventBridge, SNS, Step Functions,
// Kinesis, or custom formats) to typed handlers. It handles envelope parsing, payload
// unmarshaling, validation, and response semantics — letting you focus on business logic.
//
// # Quick Start
//
// Define a procedure (no return value) for fire-and-forget patterns:
//
//	type UserCreatedProc struct {
//	    onboarding Onboarding
//	}
//
//	type UserCreatedPayload struct {
//	    UserID string `json:"user_id"`
//	    Email  string `json:"email"`
//	}
//
//	func (p *UserCreatedProc) Run(ctx context.Context, payload UserCreatedPayload) error {
//	    return p.onboarding.RegisterUser(ctx, payload.UserID, payload.Email)
//	}
//
// Or define a function (returns result) for request-response patterns:
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
//
// Create a router, add sources, and register handlers:
//
//	r := dispatch.New()
//
//	r.AddSource(myEventBridgeSource)
//
//	dispatch.RegisterProc(r, "my.service:user/created", &UserCreatedProc{onboarding})
//	dispatch.RegisterFunc(r, "my.service:lookup-user", &LookupUserFunc{client})
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
//   - Handlers: Pure business logic with typed payloads (Proc or Func)
//
// This separation allows:
//   - Multiple message formats on a single queue
//   - Transport-agnostic handler code
//   - Consistent observability via hooks
//   - Easy testing with mock sources
//
// # Proc vs Func
//
// The package provides two handler patterns:
//
//   - Proc[T]: For fire-and-forget operations (Run returns only error)
//   - Func[T, R]: For request-response operations (Call returns result and error)
//
// Sources can set Message.Replier to enable response handling. When a Replier is
// present, the router automatically calls Replier.Reply on success (with the
// marshaled result for Func, or {} for Proc) or Replier.Fail on error.
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
//	    Parse(raw []byte) (Message, error)
//	}
//
// Sources are evaluated in registration order using their Discriminator. Once a matching
// source is found, the router calls its Parse method. If Parse returns an error, processing
// stops and the error is returned; the router does not fall back to other sources.
//
// The Message struct contains:
//   - Key: routing key to match against registered handlers
//   - Version: optional schema version for version-aware routing
//   - Payload: raw JSON to unmarshal into the handler's type
//   - Replier: optional interface for request-response patterns
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
//	func (s *mySource) Parse(raw []byte) (dispatch.Message, error) {
//	    var env struct {
//	        Type    string          `json:"type"`
//	        Payload json.RawMessage `json:"payload"`
//	    }
//	    if err := json.Unmarshal(raw, &env); err != nil {
//	        return dispatch.Message{}, err
//	    }
//	    if env.Type == "" {
//	        return dispatch.Message{}, errors.New("missing type field")
//	    }
//	    return dispatch.Message{
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
// Procedures implement the Proc interface (fire-and-forget):
//
//	type Proc[T any] interface {
//	    Run(ctx context.Context, payload T) error
//	}
//
// Functions implement the Func interface (request-response):
//
//	type Func[T, R any] interface {
//	    Call(ctx context.Context, payload T) (R, error)
//	}
//
// The router automatically:
//   - Unmarshals the JSON payload to the handler's type
//   - Validates the payload if it implements Validate() error
//   - Calls the handler with the typed payload
//   - Sends the response via Replier if present
//
// Use ProcFunc/FuncFunc for simple cases without a struct:
//
//	dispatch.RegisterProcFunc(r, "ping", func(ctx context.Context, p PingPayload) error {
//	    return nil
//	})
//
//	dispatch.RegisterFuncFunc(r, "lookup", func(ctx context.Context, in Input) (*Result, error) {
//	    return &Result{...}, nil
//	})
//
// # Replier
//
// Sources can provide a Replier in Message for transport-specific response handling.
// For example, Step Functions requires SendTaskSuccess or SendTaskFailure after processing:
//
//	type sfnReplier struct {
//	    sfn   SFNClient
//	    token string
//	}
//
//	func (r *sfnReplier) Reply(ctx context.Context, result json.RawMessage) error {
//	    return r.sfn.SendTaskSuccess(ctx, r.token, result)
//	}
//
//	func (r *sfnReplier) Fail(ctx context.Context, err error) error {
//	    return r.sfn.SendTaskFailure(ctx, r.token, err)
//	}
//
//	func (s *sfnSource) Parse(raw []byte) (dispatch.Message, error) {
//	    // ... parse envelope ...
//	    return dispatch.Message{
//	        Key:     taskType,
//	        Payload: payload,
//	        Replier: &sfnReplier{sfn: s.sfn, token: token},
//	    }, nil
//	}
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
// # Validation
//
// Payloads that implement Validate() error are automatically validated
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
// AddSource, AddGroup, or RegisterProc/RegisterFunc after calling Process.
package dispatch
