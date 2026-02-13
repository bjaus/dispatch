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
// in a single map. Returns the result (nil for Procs) and any error.
type invoker func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)

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

	lastMatch atomic.Value // stores sourceRef
}

// sourceRef identifies a source by its position in the router.
type sourceRef struct {
	groupIdx  int // -1 for default sources
	sourceIdx int
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

// RegisterProc adds a procedure (no result) for a routing key. The key must
// match the Key field returned by a source's Parse method.
//
// This is a package-level function (not a method) due to Go generics limitations:
// methods cannot have type parameters independent of the receiver.
//
// Example:
//
//	dispatch.RegisterProc(r, "user/created", &UserCreatedProc{db: db})
//	dispatch.RegisterProc(r, "user/deleted", &UserDeletedProc{db: db})
func RegisterProc[T any](r *Router, key string, p Proc[T]) {
	r.handlers[key] = func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		data, err := unmarshalAndValidate[T](payload)
		if err != nil {
			return nil, err
		}
		if err := p.Run(ctx, data); err != nil {
			return nil, err
		}
		// Procs return empty JSON object for Replier.Reply
		return []byte("{}"), nil
	}
}

// RegisterFunc adds a function (returns result) for a routing key. The key must
// match the Key field returned by a source's Parse method.
//
// Example:
//
//	dispatch.RegisterFunc(r, "lookup-user", &LookupUserFunc{client: client})
func RegisterFunc[T, R any](r *Router, key string, f Func[T, R]) {
	r.handlers[key] = func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		data, err := unmarshalAndValidate[T](payload)
		if err != nil {
			return nil, err
		}
		result, err := f.Call(ctx, data)
		if err != nil {
			return nil, err
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("marshal result: %w", err)
		}
		return resultJSON, nil
	}
}

// unmarshalAndValidate unmarshals JSON and validates if the type implements validatable.
func unmarshalAndValidate[T any](payload json.RawMessage) (T, error) {
	var data T
	if err := json.Unmarshal(payload, &data); err != nil {
		return data, &unmarshalError{err: err}
	}

	if v, ok := any(data).(validatable); ok {
		if err := v.Validate(); err != nil {
			return data, &validationError{err: err}
		}
	} else if v, ok := any(&data).(validatable); ok {
		if err := v.Validate(); err != nil {
			return data, &validationError{err: err}
		}
	}

	return data, nil
}

// RegisterProcFunc is a convenience function for registering a procedure function.
//
// Example:
//
//	dispatch.RegisterProcFunc(r, "user/created", func(ctx context.Context, p Payload) error {
//	    return nil
//	})
func RegisterProcFunc[T any](r *Router, key string, fn func(ctx context.Context, payload T) error) {
	RegisterProc(r, key, ProcFunc[T](fn))
}

// RegisterFuncFunc is a convenience function for registering a function function.
//
// Example:
//
//	dispatch.RegisterFuncFunc(r, "lookup-user", func(ctx context.Context, in Input) (*Result, error) {
//	    return &Result{...}, nil
//	})
func RegisterFuncFunc[T, R any](r *Router, key string, fn func(ctx context.Context, payload T) (R, error)) {
	RegisterFunc(r, key, FuncFunc[T, R](fn))
}

// Process parses the raw message, routes to the appropriate handler, and
// sends responses via the Replier if present.
//
// The processing flow:
//  1. Use discriminators to find a matching source
//  2. Parse the message with the matched source
//  3. Look up the handler by the parsed routing key
//  4. Unmarshal the payload to the handler's type
//  5. Validate the payload if it implements Validatable
//  6. Call the handler
//  7. Send response via Replier if present (success or failure)
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
	msg, err := source.Parse(raw)
	if err != nil {
		return r.handleParseError(ctx, source, err)
	}

	sourceName := source.Name()

	// OnParse: global, then source
	ctx = r.callOnParse(ctx, source, sourceName, msg.Key)

	// Look up handler
	handler, found := r.handlers[msg.Key]
	if !found {
		return r.handleNoHandler(ctx, source, sourceName, msg.Key, msg.Replier)
	}

	// OnDispatch: global, then source
	r.callOnDispatch(ctx, source, sourceName, msg.Key)

	// Execute handler
	start := time.Now()
	result, err := handler(ctx, msg.Payload)
	duration := time.Since(start)

	// Handle unmarshal and validation errors specially
	var uerr *unmarshalError
	if errors.As(err, &uerr) {
		return r.handleUnmarshalError(ctx, source, sourceName, msg.Key, uerr.err, msg.Replier)
	}
	var verr *validationError
	if errors.As(err, &verr) {
		return r.handleValidationError(ctx, source, sourceName, msg.Key, verr.err, msg.Replier)
	}

	// OnSuccess/OnFailure: global, then source
	if err != nil {
		r.callOnFailure(ctx, source, sourceName, msg.Key, err, duration)
	} else {
		r.callOnSuccess(ctx, source, sourceName, msg.Key, duration)
	}

	// Send response via Replier if present
	if msg.Replier != nil {
		if err != nil {
			return msg.Replier.Fail(ctx, err)
		}
		return msg.Replier.Reply(ctx, result)
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
func (r *Router) match(raw []byte) Source {
	cache := newViewCache(raw)

	if v := r.lastMatch.Load(); v != nil {
		if ref, ok := v.(sourceRef); ok {
			if src := r.trySource(cache, ref); src != nil {
				return src
			}
		}
	}

	return r.matchAll(cache)
}

// trySource attempts to match the source at the given position.
func (r *Router) trySource(cache *viewCache, ref sourceRef) Source {
	if ref.groupIdx == -1 {
		if ref.sourceIdx >= len(r.defaultSources) {
			return nil
		}
		view, ok := cache.get(r.defaultInspector)
		if !ok {
			return nil
		}
		src := r.defaultSources[ref.sourceIdx]
		if src.Discriminator().Match(view) {
			return src
		}
		return nil
	}

	if ref.groupIdx >= len(r.groups) {
		return nil
	}
	g := r.groups[ref.groupIdx]
	if ref.sourceIdx >= len(g.sources) {
		return nil
	}
	view, ok := cache.get(g.inspector)
	if !ok {
		return nil
	}
	src := g.sources[ref.sourceIdx]
	if src.Discriminator().Match(view) {
		return src
	}
	return nil
}

// matchAll searches all groups for a matching source.
func (r *Router) matchAll(cache *viewCache) Source {
	if len(r.defaultSources) > 0 {
		if view, ok := cache.get(r.defaultInspector); ok {
			for i, src := range r.defaultSources {
				if src.Discriminator().Match(view) {
					r.lastMatch.Store(sourceRef{groupIdx: -1, sourceIdx: i})
					return src
				}
			}
		}
	}

	for gi, g := range r.groups {
		view, ok := cache.get(g.inspector)
		if !ok {
			continue
		}
		for si, src := range g.sources {
			if src.Discriminator().Match(view) {
				r.lastMatch.Store(sourceRef{groupIdx: gi, sourceIdx: si})
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
func (r *Router) handleNoHandler(ctx context.Context, source Source, sourceName, key string, replier Replier) error {
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

	var resultErr error
	switch {
	case len(errs) > 0:
		resultErr = errs[0]
	case len(r.hooks.onNoHandler) == 0:
		resultErr = fmt.Errorf("no handler for key: %s", key)
	}

	if resultErr != nil && replier != nil {
		return replier.Fail(ctx, resultErr)
	}

	return resultErr
}

// handleUnmarshalError handles JSON unmarshal errors.
func (r *Router) handleUnmarshalError(ctx context.Context, source Source, sourceName, key string, err error, replier Replier) error {
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

	if resultErr != nil && replier != nil {
		return replier.Fail(ctx, resultErr)
	}

	return resultErr
}

// handleValidationError handles payload validation errors.
func (r *Router) handleValidationError(ctx context.Context, source Source, sourceName, key string, err error, replier Replier) error {
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

	if resultErr != nil && replier != nil {
		return replier.Fail(ctx, resultErr)
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
