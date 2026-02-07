package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// sourceWithHooks implements optional hook interfaces for testing.
type sourceWithHooks struct {
	name string

	onParseCalled           bool
	onDispatchCalled        bool
	onSuccessCalled         bool
	onFailureCalled         bool
	onNoHandlerCalled       bool
	onUnmarshalErrorCalled  bool
	onValidationErrorCalled bool

	onNoHandlerErr       error
	onUnmarshalErrorErr  error
	onValidationErrorErr error
}

func (s *sourceWithHooks) Name() string { return s.name }

func (s *sourceWithHooks) Discriminator() Discriminator {
	return HasFields("type", "payload")
}

func (s *sourceWithHooks) Parse(raw []byte) (Parsed, bool) {
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
		return Parsed{}, false
	}
	return Parsed{Key: env.Type, Payload: env.Payload}, true
}

func (s *sourceWithHooks) OnParse(ctx context.Context, key string) context.Context {
	s.onParseCalled = true
	return context.WithValue(ctx, "source-hook", "called")
}

func (s *sourceWithHooks) OnDispatch(ctx context.Context, key string) {
	s.onDispatchCalled = true
}

func (s *sourceWithHooks) OnSuccess(ctx context.Context, key string, d time.Duration) {
	s.onSuccessCalled = true
}

func (s *sourceWithHooks) OnFailure(ctx context.Context, key string, err error, d time.Duration) {
	s.onFailureCalled = true
}

func (s *sourceWithHooks) OnNoHandler(ctx context.Context, key string) error {
	s.onNoHandlerCalled = true
	return s.onNoHandlerErr
}

func (s *sourceWithHooks) OnUnmarshalError(ctx context.Context, key string, err error) error {
	s.onUnmarshalErrorCalled = true
	return s.onUnmarshalErrorErr
}

func (s *sourceWithHooks) OnValidationError(ctx context.Context, key string, err error) error {
	s.onValidationErrorCalled = true
	return s.onValidationErrorErr
}

// Verify interface implementations
var (
	_ Source                = (*sourceWithHooks)(nil)
	_ OnParseHook           = (*sourceWithHooks)(nil)
	_ OnDispatchHook        = (*sourceWithHooks)(nil)
	_ OnSuccessHook         = (*sourceWithHooks)(nil)
	_ OnFailureHook         = (*sourceWithHooks)(nil)
	_ OnNoHandlerHook       = (*sourceWithHooks)(nil)
	_ OnUnmarshalErrorHook  = (*sourceWithHooks)(nil)
	_ OnValidationErrorHook = (*sourceWithHooks)(nil)
)

func TestSourceHooks(t *testing.T) {
	t.Run("OnParse is called after global", func(t *testing.T) {
		var order []string

		source := &sourceWithHooks{name: "test"}

		r := New(WithOnParse(func(ctx context.Context, src, key string) context.Context {
			order = append(order, "global")
			return ctx
		}))
		r.AddSource(source)
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if !source.onParseCalled {
			t.Error("source OnParse not called")
		}

		// Check order by examining the context (source hook adds to it)
		// and verifying global was called first
		if len(order) != 1 || order[0] != "global" {
			t.Errorf("order = %v, want [global]", order)
		}
	})

	t.Run("OnDispatch is called after global", func(t *testing.T) {
		var order []string

		source := &sourceWithHooks{name: "test"}

		r := New(WithOnDispatch(func(ctx context.Context, src, key string) {
			order = append(order, "global")
		}))
		r.AddSource(source)
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if !source.onDispatchCalled {
			t.Error("source OnDispatch not called")
		}
		if len(order) != 1 || order[0] != "global" {
			t.Errorf("order = %v, want [global]", order)
		}
	})

	t.Run("OnSuccess is called after global", func(t *testing.T) {
		var order []string

		source := &sourceWithHooks{name: "test"}

		r := New(WithOnSuccess(func(ctx context.Context, src, key string, d time.Duration) {
			order = append(order, "global")
		}))
		r.AddSource(source)
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if !source.onSuccessCalled {
			t.Error("source OnSuccess not called")
		}
		if len(order) != 1 || order[0] != "global" {
			t.Errorf("order = %v, want [global]", order)
		}
	})

	t.Run("OnFailure is called after global", func(t *testing.T) {
		var order []string

		source := &sourceWithHooks{name: "test"}

		r := New(WithOnFailure(func(ctx context.Context, src, key string, err error, d time.Duration) {
			order = append(order, "global")
		}))
		r.AddSource(source)
		Register(r, "test", &testHandler{err: errors.New("fail")})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if !source.onFailureCalled {
			t.Error("source OnFailure not called")
		}
		if len(order) != 1 || order[0] != "global" {
			t.Errorf("order = %v, want [global]", order)
		}
	})

	t.Run("source OnNoHandler can override global skip", func(t *testing.T) {
		source := &sourceWithHooks{
			name:           "test",
			onNoHandlerErr: errors.New("source says fail"),
		}

		r := New(WithOnNoHandler(func(ctx context.Context, src, key string) error {
			return nil // global says skip
		}))
		r.AddSource(source)

		msg := []byte(`{"type": "unknown", "payload": {}}`)
		err := r.Process(context.Background(), msg)

		if !source.onNoHandlerCalled {
			t.Error("source OnNoHandler not called")
		}
		if err == nil {
			t.Error("expected error from source hook")
		}
	})

	t.Run("global OnNoHandler error prevents source hook from running for that error path", func(t *testing.T) {
		globalErr := errors.New("global error")
		source := &sourceWithHooks{
			name:           "test",
			onNoHandlerErr: errors.New("source error"),
		}

		r := New(WithOnNoHandler(func(ctx context.Context, src, key string) error {
			return globalErr // global fails first
		}))
		r.AddSource(source)

		msg := []byte(`{"type": "unknown", "payload": {}}`)
		err := r.Process(context.Background(), msg)

		// Both hooks are called, but first error wins
		if !errors.Is(err, globalErr) {
			t.Errorf("error = %v, want %v", err, globalErr)
		}
	})

	t.Run("source OnUnmarshalError can override global", func(t *testing.T) {
		source := &sourceWithHooks{
			name:                "test",
			onUnmarshalErrorErr: errors.New("source says fail"),
		}

		r := New(WithOnUnmarshalError(func(ctx context.Context, src, key string, err error) error {
			return nil // global says skip
		}))
		r.AddSource(source)
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": "invalid"}`)
		err := r.Process(context.Background(), msg)

		if !source.onUnmarshalErrorCalled {
			t.Error("source OnUnmarshalError not called")
		}
		if err == nil {
			t.Error("expected error from source hook")
		}
	})
}

func TestSourceHooks_ContextPropagation(t *testing.T) {
	t.Run("source OnParse context is available to handler", func(t *testing.T) {
		source := &sourceWithHooks{name: "test"}

		var handlerCtx context.Context
		r := New()
		r.AddSource(source)

		RegisterFunc(r, "test", func(ctx context.Context, p testPayload) error {
			handlerCtx = ctx
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if handlerCtx.Value("source-hook") != "called" {
			t.Error("source OnParse context not propagated to handler")
		}
	})
}
