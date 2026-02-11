package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
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
//  2. Add sources with AddSource (or AddGroup for custom inspectors)
//  3. Register handlers with Register
//  4. Process messages with Process
//
// Router is safe for concurrent use after configuration. Do not call AddSource,
// AddGroup, or Register after calling Process.
type Router struct {
	defaultInspector Inspector
	defaultSources   []Source
	groups           []group
	handlers         map[string]invoker
	hooks            hooks

	// Adaptive ordering: try last successful source first
	lastMatch atomic.Value // stores string
}

// group holds sources that share an inspector.
type group struct {
	inspector Inspector
	sources   []Source
}

// New creates a Router with the given options.
//
// By default, the router uses JSONInspector for source matching. Use
// WithInspector to override.
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
		defaultInspector: JSONInspector(),
		handlers:         make(map[string]invoker),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithInspector sets the default inspector for sources added with AddSource.
func WithInspector(i Inspector) Option {
	return func(r *Router) {
		r.defaultInspector = i
	}
}

// AddSource registers a source to the default inspector group. Sources are
// matched using their Discriminator, then parsed in registration order.
//
// Example:
//
//	r.AddSource(eventBridgeSource)
//	r.AddSource(snsSource)
//	r.AddSource(sfnSource)
func (r *Router) AddSource(s Source) {
	r.defaultSources = append(r.defaultSources, s)
}

// AddGroup registers sources with a custom inspector. Use this when you have
// sources that use a different message format (e.g., protobuf).
//
// Groups are checked after the default group, in registration order.
//
// Example:
//
//	r.AddGroup(protoInspector, grpcSource, kafkaSource)
func (r *Router) AddGroup(inspector Inspector, sources ...Source) {
	r.groups = append(r.groups, group{inspector: inspector, sources: sources})
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

		if v, ok := any(data).(validatable); ok {
			if err := v.Validate(); err != nil {
				return &validationError{err: err}
			}
		} else if v, ok := any(&data).(validatable); ok {
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
//  1. Use discriminators to find a matching source
//  2. Parse the message with the matched source
//  3. Look up the handler by the parsed routing key
//  4. Unmarshal the payload to the handler's type
//  5. Validate the payload if it implements Validatable
//  6. Call the handler
//  7. Call the source's Complete callback if provided
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
	// Find matching source using discriminators
	source := r.match(raw)
	if source == nil {
		return r.handleNoSource(ctx, raw)
	}

	// Parse with matched source
	parsed, err := source.Parse(raw)
	if err != nil {
		return r.handleParseError(ctx, source, err)
	}

	sourceName := source.Name()

	// OnParse: global, then source
	ctx = r.callOnParse(ctx, source, sourceName, parsed.Key)

	// Look up handler
	handler, found := r.handlers[parsed.Key]
	if !found {
		return r.handleNoHandler(ctx, source, sourceName, parsed.Key)
	}

	// OnDispatch: global, then source
	r.callOnDispatch(ctx, source, sourceName, parsed.Key)

	// Execute handler
	start := time.Now()
	err = handler(ctx, parsed.Payload)
	duration := time.Since(start)

	// Handle unmarshal and validation errors specially
	var uerr *unmarshalError
	if errors.As(err, &uerr) {
		return r.handleUnmarshalError(ctx, source, sourceName, parsed.Key, uerr.err, parsed.Complete)
	}
	var verr *validationError
	if errors.As(err, &verr) {
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

// viewCache caches parsed views per inspector to avoid re-parsing the same
// raw bytes multiple times during source matching.
type viewCache struct {
	raw   []byte
	views map[Inspector]viewResult
}

type viewResult struct {
	view View
	ok   bool
}

func newViewCache(raw []byte) *viewCache {
	return &viewCache{
		raw:   raw,
		views: make(map[Inspector]viewResult),
	}
}

// get returns a cached view or parses and caches it.
func (c *viewCache) get(insp Inspector) (View, bool) {
	if result, ok := c.views[insp]; ok {
		return result.view, result.ok
	}

	view, err := insp.Inspect(c.raw)
	if err != nil {
		c.views[insp] = viewResult{ok: false}
		return nil, false
	}

	c.views[insp] = viewResult{view: view, ok: true}
	return view, true
}

// match finds a source whose discriminator matches the raw message.
// Uses adaptive ordering to try the last successful source first.
func (r *Router) match(raw []byte) Source {
	cache := newViewCache(raw)

	// Try last successful source first (fast path)
	if v := r.lastMatch.Load(); v != nil {
		if lastMatch, ok := v.(string); ok && lastMatch != "" {
			if src := r.trySource(cache, lastMatch); src != nil {
				return src
			}
		}
	}

	// Full search through all groups
	src := r.matchAll(cache)
	if src != nil {
		r.lastMatch.Store(src.Name())
	}
	return src
}

// trySource attempts to match a specific source by name.
func (r *Router) trySource(cache *viewCache, name string) Source {
	// Check default sources
	if len(r.defaultSources) > 0 {
		if view, ok := cache.get(r.defaultInspector); ok {
			for _, src := range r.defaultSources {
				if src.Name() == name && src.Discriminator().Match(view) {
					return src
				}
			}
		}
	}

	// Check custom groups
	for _, g := range r.groups {
		view, ok := cache.get(g.inspector)
		if !ok {
			continue
		}
		for _, src := range g.sources {
			if src.Name() == name && src.Discriminator().Match(view) {
				return src
			}
		}
	}

	return nil
}

// matchAll searches all groups for a matching source.
func (r *Router) matchAll(cache *viewCache) Source {
	// Try default group first
	if len(r.defaultSources) > 0 {
		if view, ok := cache.get(r.defaultInspector); ok {
			for _, src := range r.defaultSources {
				if src.Discriminator().Match(view) {
					return src
				}
			}
		}
	}

	// Try custom groups in order
	for _, g := range r.groups {
		view, ok := cache.get(g.inspector)
		if !ok {
			continue
		}
		for _, src := range g.sources {
			if src.Discriminator().Match(view) {
				return src
			}
		}
	}

	return nil
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

// handleParseError handles the case when a source's Parse method returns an error.
func (r *Router) handleParseError(ctx context.Context, source Source, parseErr error) error {
	sourceName := source.Name()
	for _, fn := range r.hooks.onParseError {
		if err := fn(ctx, sourceName, parseErr); err != nil {
			return err
		}
	}
	if len(r.hooks.onParseError) > 0 {
		return nil
	}
	return fmt.Errorf("parse failed for source %s: %w", sourceName, parseErr)
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
//
//nolint:dupl // Similar to handleValidationError; intentional pattern
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

	var resultErr error
	switch {
	case len(errs) > 0:
		resultErr = errs[0]
	case len(r.hooks.onUnmarshalError) == 0:
		resultErr = fmt.Errorf("unmarshal payload: %w", err)
	}

	if complete != nil {
		return complete(ctx, resultErr)
	}

	return resultErr
}

// handleValidationError handles payload validation errors.
//
//nolint:dupl // Similar to handleUnmarshalError; intentional pattern
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

	var resultErr error
	switch {
	case len(errs) > 0:
		resultErr = errs[0]
	case len(r.hooks.onValidationError) == 0:
		resultErr = fmt.Errorf("validate payload: %w", err)
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
