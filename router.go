package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// validatable is the interface for payload validation.
// Compatible with github.com/go-ozzo/ozzo-validation/v4.
type validatable interface {
	Validate() error
}

// invoker wraps a typed handler so we can store handlers of different types
// in a single map.
type invoker func(ctx context.Context, payload json.RawMessage) error

// Router dispatches messages to registered handlers based on routing keys.
//
// Usage:
//  1. Create a router with New
//  2. Add sources with AddSource
//  3. Register handlers with Register
//  4. Process messages with Process
//
// Router is safe for concurrent use after configuration. Do not call AddSource
// or Register after calling Process.
type Router struct {
	sources  []Source
	handlers map[string]invoker
	hooks    hooks
}

// New creates a Router with the given options.
//
// Example:
//
//	r := dispatch.New(
//	    dispatch.WithOnParse(func(ctx context.Context, source, key string) context.Context {
//	        return logx.WithCtx(ctx, slog.String("source", source))
//	    }),
//	    dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
//	        metrics.Timing("dispatch.success", d)
//	    }),
//	)
func New(opts ...Option) *Router {
	r := &Router{
		handlers: make(map[string]invoker),
	}
	for _, opt := range opts {
		opt(&r.hooks)
	}
	return r
}

// AddSource registers a source. Sources are tried in registration order during
// Process until one successfully parses the message.
//
// Example:
//
//	r.AddSource(eventBridgeSource)
//	r.AddSource(snsSource)
//	r.AddSource(sfnSource)
func (r *Router) AddSource(s Source) {
	r.sources = append(r.sources, s)
}

// Register adds a handler for a routing key. The key must match the Key field
// returned by a source's Parse method.
//
// This is a package-level function (not a method) due to Go generics limitations:
// methods cannot have type parameters independent of the receiver.
//
// Example:
//
//	dispatch.Register(r, "user/created", &UserCreatedHandler{db: db})
//	dispatch.Register(r, "user/deleted", &UserDeletedHandler{db: db})
func Register[T any](r *Router, key string, h Handler[T]) {
	r.handlers[key] = func(ctx context.Context, payload json.RawMessage) error {
		var data T
		if err := json.Unmarshal(payload, &data); err != nil {
			return &unmarshalError{err: err}
		}

		if v, ok := any(&data).(validatable); ok {
			if err := v.Validate(); err != nil {
				return &validationError{err: err}
			}
		}

		return h.Handle(ctx, data)
	}
}

// RegisterFunc is a convenience function for registering a handler function.
//
// Example:
//
//	dispatch.RegisterFunc(r, "ping", func(ctx context.Context, p PingPayload) error {
//	    return nil
//	})
func RegisterFunc[T any](r *Router, key string, fn func(ctx context.Context, payload T) error) {
	Register(r, key, HandlerFunc[T](fn))
}

// Process parses the raw message, routes to the appropriate handler, and
// executes completion callbacks.
//
// The processing flow:
//  1. Try each source's Parse method until one returns true
//  2. Look up the handler by the parsed routing key
//  3. Unmarshal the payload to the handler's type
//  4. Validate the payload if it implements Validatable
//  5. Call the handler
//  6. Call the source's Complete callback if provided
//
// Hooks are called at appropriate points throughout this flow.
//
// Example:
//
//	// In an SQS consumer
//	func (s *Subscriber) ProcessMessage(ctx context.Context, msg sqs.Message) error {
//	    return s.router.Process(ctx, []byte(*msg.Body))
//	}
//
//	// In a Lambda handler
//	func handler(ctx context.Context, event json.RawMessage) error {
//	    return router.Process(ctx, event)
//	}
func (r *Router) Process(ctx context.Context, raw []byte) error {
	// Try each source until one matches
	var parsed Parsed
	var source Source
	for _, src := range r.sources {
		if p, ok := src.Parse(raw); ok {
			parsed = p
			source = src
			break
		}
	}

	if source == nil {
		return r.handleNoSource(ctx, raw)
	}

	sourceName := source.Name()

	// OnParse: global, then source
	ctx = r.callOnParse(ctx, source, sourceName, parsed.Key)

	// Look up handler
	handler, ok := r.handlers[parsed.Key]
	if !ok {
		return r.handleNoHandler(ctx, source, sourceName, parsed.Key)
	}

	// OnDispatch: global, then source
	r.callOnDispatch(ctx, source, sourceName, parsed.Key)

	// Execute handler
	start := time.Now()
	err := handler(ctx, parsed.Payload)
	duration := time.Since(start)

	// Handle unmarshal and validation errors specially
	if uerr, ok := err.(*unmarshalError); ok {
		return r.handleUnmarshalError(ctx, source, sourceName, parsed.Key, uerr.err, parsed.Complete)
	}
	if verr, ok := err.(*validationError); ok {
		return r.handleValidationError(ctx, source, sourceName, parsed.Key, verr.err, parsed.Complete)
	}

	// OnSuccess/OnFailure: global, then source
	if err != nil {
		r.callOnFailure(ctx, source, sourceName, parsed.Key, err, duration)
	} else {
		r.callOnSuccess(ctx, source, sourceName, parsed.Key, duration)
	}

	// Complete callback (e.g., Step Functions)
	if parsed.Complete != nil {
		return parsed.Complete(ctx, err)
	}

	return err
}

// callOnParse calls global and source OnParse hooks.
func (r *Router) callOnParse(ctx context.Context, source Source, sourceName, key string) context.Context {
	for _, fn := range r.hooks.onParse {
		ctx = fn(ctx, sourceName, key)
	}
	if h, ok := source.(OnParseHook); ok {
		ctx = h.OnParse(ctx, key)
	}
	return ctx
}

// callOnDispatch calls global and source OnDispatch hooks.
func (r *Router) callOnDispatch(ctx context.Context, source Source, sourceName, key string) {
	for _, fn := range r.hooks.onDispatch {
		fn(ctx, sourceName, key)
	}
	if h, ok := source.(OnDispatchHook); ok {
		h.OnDispatch(ctx, key)
	}
}

// callOnSuccess calls global and source OnSuccess hooks.
func (r *Router) callOnSuccess(ctx context.Context, source Source, sourceName, key string, duration time.Duration) {
	for _, fn := range r.hooks.onSuccess {
		fn(ctx, sourceName, key, duration)
	}
	if h, ok := source.(OnSuccessHook); ok {
		h.OnSuccess(ctx, key, duration)
	}
}

// callOnFailure calls global and source OnFailure hooks.
func (r *Router) callOnFailure(ctx context.Context, source Source, sourceName, key string, err error, duration time.Duration) {
	for _, fn := range r.hooks.onFailure {
		fn(ctx, sourceName, key, err, duration)
	}
	if h, ok := source.(OnFailureHook); ok {
		h.OnFailure(ctx, key, err, duration)
	}
}

// handleNoSource handles the case when no source matches.
func (r *Router) handleNoSource(ctx context.Context, raw []byte) error {
	for _, fn := range r.hooks.onNoSource {
		if err := fn(ctx, raw); err != nil {
			return err
		}
	}
	if len(r.hooks.onNoSource) > 0 {
		return nil
	}
	return fmt.Errorf("no source matched message")
}

// handleNoHandler handles the case when no handler is registered.
func (r *Router) handleNoHandler(ctx context.Context, source Source, sourceName, key string) error {
	var errs []error

	for _, fn := range r.hooks.onNoHandler {
		if err := fn(ctx, sourceName, key); err != nil {
			errs = append(errs, err)
		}
	}

	if h, ok := source.(OnNoHandlerHook); ok {
		if err := h.OnNoHandler(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}

	// Default behavior if no hooks set
	if len(r.hooks.onNoHandler) == 0 {
		return fmt.Errorf("no handler for key: %s", key)
	}

	return nil
}

// handleUnmarshalError handles JSON unmarshal errors.
func (r *Router) handleUnmarshalError(ctx context.Context, source Source, sourceName, key string, err error, complete func(context.Context, error) error) error {
	var errs []error

	for _, fn := range r.hooks.onUnmarshalError {
		if herr := fn(ctx, sourceName, key, err); herr != nil {
			errs = append(errs, herr)
		}
	}

	if h, ok := source.(OnUnmarshalErrorHook); ok {
		if herr := h.OnUnmarshalError(ctx, key, err); herr != nil {
			errs = append(errs, herr)
		}
	}

	resultErr := err
	if len(errs) > 0 {
		resultErr = errs[0]
	} else if len(r.hooks.onUnmarshalError) == 0 {
		resultErr = fmt.Errorf("unmarshal payload: %w", err)
	} else {
		resultErr = nil
	}

	if complete != nil {
		return complete(ctx, resultErr)
	}

	return resultErr
}

// handleValidationError handles payload validation errors.
func (r *Router) handleValidationError(ctx context.Context, source Source, sourceName, key string, err error, complete func(context.Context, error) error) error {
	var errs []error

	for _, fn := range r.hooks.onValidationError {
		if herr := fn(ctx, sourceName, key, err); herr != nil {
			errs = append(errs, herr)
		}
	}

	if h, ok := source.(OnValidationErrorHook); ok {
		if herr := h.OnValidationError(ctx, key, err); herr != nil {
			errs = append(errs, herr)
		}
	}

	resultErr := err
	if len(errs) > 0 {
		resultErr = errs[0]
	} else if len(r.hooks.onValidationError) == 0 {
		resultErr = fmt.Errorf("validate payload: %w", err)
	} else {
		resultErr = nil
	}

	if complete != nil {
		return complete(ctx, resultErr)
	}

	return resultErr
}

// unmarshalError wraps unmarshal errors so we can identify them.
type unmarshalError struct {
	err error
}

func (e *unmarshalError) Error() string { return e.err.Error() }
func (e *unmarshalError) Unwrap() error { return e.err }

// validationError wraps validation errors so we can identify them.
type validationError struct {
	err error
}

func (e *validationError) Error() string { return e.err.Error() }
func (e *validationError) Unwrap() error { return e.err }
