package dispatch

import (
	"context"
	"time"
)

// OnParseFunc is called after a source successfully parses a message.
// Use this to enrich the context with logging fields or trace spans.
// The returned context is used for the rest of the request.
type OnParseFunc func(ctx context.Context, source, key string) context.Context

// OnDispatchFunc is called just before the handler executes.
type OnDispatchFunc func(ctx context.Context, source, key string)

// OnSuccessFunc is called after the handler completes successfully.
type OnSuccessFunc func(ctx context.Context, source, key string, duration time.Duration)

// OnFailureFunc is called after the handler fails.
type OnFailureFunc func(ctx context.Context, source, key string, err error, duration time.Duration)

// OnNoSourceFunc is called when no source can parse the message.
// Return nil to skip the message, return an error to fail.
type OnNoSourceFunc func(ctx context.Context, raw []byte) error

// OnNoHandlerFunc is called when no handler is registered for the routing key.
// Return nil to skip, return an error to fail.
type OnNoHandlerFunc func(ctx context.Context, source, key string) error

// OnUnmarshalErrorFunc is called when JSON unmarshaling fails.
// Return nil to skip, return an error to fail.
type OnUnmarshalErrorFunc func(ctx context.Context, source, key string, err error) error

// OnValidationErrorFunc is called when payload validation fails.
// Return nil to skip, return an error to fail.
type OnValidationErrorFunc func(ctx context.Context, source, key string, err error) error

// hooks holds all configured hook functions.
type hooks struct {
	onParse           []OnParseFunc
	onDispatch        []OnDispatchFunc
	onSuccess         []OnSuccessFunc
	onFailure         []OnFailureFunc
	onNoSource        []OnNoSourceFunc
	onNoHandler       []OnNoHandlerFunc
	onUnmarshalError  []OnUnmarshalErrorFunc
	onValidationError []OnValidationErrorFunc
}

// Option configures hook behavior.
type Option func(*hooks)

// WithOnParse adds a hook called after a source successfully parses a message.
// Multiple hooks are called in order, with context chaining through each.
//
// Example:
//
//	dispatch.WithOnParse(func(ctx context.Context, source, key string) context.Context {
//	    return logx.WithCtx(ctx, slog.String("source", source))
//	})
func WithOnParse(fn OnParseFunc) Option {
	return func(h *hooks) {
		h.onParse = append(h.onParse, fn)
	}
}

// WithOnDispatch adds a hook called just before the handler executes.
// Multiple hooks are called in order.
//
// Example:
//
//	dispatch.WithOnDispatch(func(ctx context.Context, source, key string) {
//	    logger.Info(ctx, "dispatching event", "key", key)
//	})
func WithOnDispatch(fn OnDispatchFunc) Option {
	return func(h *hooks) {
		h.onDispatch = append(h.onDispatch, fn)
	}
}

// WithOnSuccess adds a hook called after the handler completes successfully.
// Multiple hooks are called in order.
//
// Example:
//
//	dispatch.WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
//	    metrics.Timing("dispatch.success", d, "source:"+source)
//	})
func WithOnSuccess(fn OnSuccessFunc) Option {
	return func(h *hooks) {
		h.onSuccess = append(h.onSuccess, fn)
	}
}

// WithOnFailure adds a hook called after the handler fails.
// Multiple hooks are called in order.
//
// Example:
//
//	dispatch.WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
//	    metrics.Incr("dispatch.failure", "source:"+source)
//	    logger.Error(ctx, "handler failed", "error", err)
//	})
func WithOnFailure(fn OnFailureFunc) Option {
	return func(h *hooks) {
		h.onFailure = append(h.onFailure, fn)
	}
}

// WithOnNoSource adds a hook called when no source can parse the message.
// Return nil to skip, return an error to fail.
// Multiple hooks are called in order; first error wins.
//
// Example:
//
//	dispatch.WithOnNoSource(func(ctx context.Context, raw []byte) error {
//	    logger.Warn(ctx, "unknown message format")
//	    return nil // skip to DLQ
//	})
func WithOnNoSource(fn OnNoSourceFunc) Option {
	return func(h *hooks) {
		h.onNoSource = append(h.onNoSource, fn)
	}
}

// WithOnNoHandler adds a hook called when no handler is registered for the key.
// Return nil to skip, return an error to fail.
// Multiple hooks are called in order; first error wins.
//
// Example:
//
//	dispatch.WithOnNoHandler(func(ctx context.Context, source, key string) error {
//	    logger.Warn(ctx, "no handler", "key", key)
//	    return nil // skip
//	})
func WithOnNoHandler(fn OnNoHandlerFunc) Option {
	return func(h *hooks) {
		h.onNoHandler = append(h.onNoHandler, fn)
	}
}

// WithOnUnmarshalError adds a hook called when JSON unmarshaling fails.
// Return nil to skip, return an error to fail.
// Multiple hooks are called in order; first error wins.
//
// Example:
//
//	dispatch.WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
//	    logger.Error(ctx, "bad payload", "error", err)
//	    return nil // skip bad payloads
//	})
func WithOnUnmarshalError(fn OnUnmarshalErrorFunc) Option {
	return func(h *hooks) {
		h.onUnmarshalError = append(h.onUnmarshalError, fn)
	}
}

// WithOnValidationError adds a hook called when payload validation fails.
// Return nil to skip, return an error to fail.
// Multiple hooks are called in order; first error wins.
//
// Example:
//
//	dispatch.WithOnValidationError(func(ctx context.Context, source, key string, err error) error {
//	    logger.Error(ctx, "validation failed", "error", err)
//	    return nil // skip invalid payloads
//	})
func WithOnValidationError(fn OnValidationErrorFunc) Option {
	return func(h *hooks) {
		h.onValidationError = append(h.onValidationError, fn)
	}
}

// OnParseHook is an optional interface that sources can implement to add
// source-specific context enrichment. Called after global OnParse hooks.
type OnParseHook interface {
	OnParse(ctx context.Context, key string) context.Context
}

// OnDispatchHook is an optional interface that sources can implement to add
// source-specific pre-dispatch behavior. Called after global OnDispatch hooks.
type OnDispatchHook interface {
	OnDispatch(ctx context.Context, key string)
}

// OnSuccessHook is an optional interface that sources can implement to add
// source-specific behavior on handler success. Called after global OnSuccess hooks.
type OnSuccessHook interface {
	OnSuccess(ctx context.Context, key string, duration time.Duration)
}

// OnFailureHook is an optional interface that sources can implement to add
// source-specific behavior on handler failure. Called after global OnFailure hooks.
type OnFailureHook interface {
	OnFailure(ctx context.Context, key string, err error, duration time.Duration)
}

// OnNoHandlerHook is an optional interface that sources can implement to add
// source-specific behavior when no handler is found. Called after global hooks;
// if either returns an error, that error is used.
type OnNoHandlerHook interface {
	OnNoHandler(ctx context.Context, key string) error
}

// OnUnmarshalErrorHook is an optional interface that sources can implement to
// add source-specific behavior on unmarshal errors. Called after global hooks;
// if either returns an error, that error is used.
type OnUnmarshalErrorHook interface {
	OnUnmarshalError(ctx context.Context, key string, err error) error
}

// OnValidationErrorHook is an optional interface that sources can implement to
// add source-specific behavior on validation errors. Called after global hooks;
// if either returns an error, that error is used.
type OnValidationErrorHook interface {
	OnValidationError(ctx context.Context, key string, err error) error
}
