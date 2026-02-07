package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type testPayload struct {
	Value string `json:"value"`
}

type testHandler struct {
	called  bool
	payload testPayload
	err     error
}

func (h *testHandler) Handle(ctx context.Context, p testPayload) error {
	h.called = true
	h.payload = p
	return h.err
}

type testSource struct {
	name      string
	shouldErr bool
}

func (s *testSource) Name() string { return s.name }

func (s *testSource) Discriminator() Discriminator {
	return HasFields("type", "payload")
}

func (s *testSource) Parse(raw []byte) (Parsed, bool) {
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Parsed{}, false
	}
	if env.Type == "" {
		return Parsed{}, false
	}
	return Parsed{Key: env.Type, Payload: env.Payload}, true
}

func TestRouter_Process(t *testing.T) {
	t.Run("dispatches to registered handler", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "test"})

		h := &testHandler{}
		Register(r, "test/event", h)

		msg := []byte(`{"type": "test/event", "payload": {"value": "hello"}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !h.called {
			t.Error("handler was not called")
		}
		if h.payload.Value != "hello" {
			t.Errorf("payload.Value = %q, want %q", h.payload.Value, "hello")
		}
	})

	t.Run("returns handler error", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "test"})

		wantErr := errors.New("handler error")
		h := &testHandler{err: wantErr}
		Register(r, "test/event", h)

		msg := []byte(`{"type": "test/event", "payload": {"value": "hello"}}`)
		err := r.Process(context.Background(), msg)

		if !errors.Is(err, wantErr) {
			t.Errorf("error = %v, want %v", err, wantErr)
		}
	})

	t.Run("returns error when no source matches", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"not": "matching"}`)
		err := r.Process(context.Background(), msg)

		if err == nil {
			t.Error("expected error, got nil")
		}
	})

	t.Run("returns error when no handler registered", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"type": "unknown/event", "payload": {}}`)
		err := r.Process(context.Background(), msg)

		if err == nil {
			t.Error("expected error, got nil")
		}
	})

	t.Run("tries sources in order", func(t *testing.T) {
		r := New()

		// First source doesn't match JSON format
		r.AddSource(SourceFunc("first", HasFields("nonexistent"), func(raw []byte) (Parsed, bool) {
			return Parsed{}, false
		}))

		// Second source matches
		r.AddSource(&testSource{name: "second"})

		h := &testHandler{}
		Register(r, "test/event", h)

		var calledSource string
		r.hooks.onParse = append(r.hooks.onParse, func(ctx context.Context, source, key string) context.Context {
			calledSource = source
			return ctx
		})

		msg := []byte(`{"type": "test/event", "payload": {"value": "test"}}`)
		_ = r.Process(context.Background(), msg)

		if calledSource != "second" {
			t.Errorf("source = %q, want %q", calledSource, "second")
		}
	})
}

func TestRouter_Groups(t *testing.T) {
	t.Run("default group uses default inspector", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "json-source"})

		h := &testHandler{}
		Register(r, "test/event", h)

		msg := []byte(`{"type": "test/event", "payload": {"value": "hello"}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !h.called {
			t.Error("handler was not called")
		}
	})

	t.Run("custom group with custom inspector", func(t *testing.T) {
		r := New()

		// Custom inspector that looks for "event" field instead of "type"
		customInspector := JSONInspector()
		customSource := SourceFunc("custom", HasFields("event", "data"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Event string          `json:"event"`
				Data  json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Event == "" {
				return Parsed{}, false
			}
			return Parsed{Key: env.Event, Payload: env.Data}, true
		})

		r.AddGroup(customInspector, customSource)

		h := &testHandler{}
		Register(r, "custom/event", h)

		msg := []byte(`{"event": "custom/event", "data": {"value": "test"}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !h.called {
			t.Error("handler was not called")
		}
	})

	t.Run("default group checked before custom groups", func(t *testing.T) {
		r := New()

		var matchedSource string

		// Default group source
		r.AddSource(SourceFunc("default", HasFields("type"), func(raw []byte) (Parsed, bool) {
			matchedSource = "default"
			var env struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		}))

		// Custom group source (also matches "type" field)
		customSource := SourceFunc("custom", HasFields("type"), func(raw []byte) (Parsed, bool) {
			matchedSource = "custom"
			var env struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})
		r.AddGroup(JSONInspector(), customSource)

		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if matchedSource != "default" {
			t.Errorf("matched source = %q, want %q", matchedSource, "default")
		}
	})

	t.Run("WithInspector overrides default inspector", func(t *testing.T) {
		// Create a custom inspector that always fails
		failingInspector := &mockInspector{err: ErrInvalidJSON}

		r := New(WithInspector(failingInspector))
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"type": "test", "payload": {}}`)
		err := r.Process(context.Background(), msg)

		// Should fail because default inspector fails
		if err == nil {
			t.Error("expected error due to failing inspector")
		}
	})
}

// mockInspector is a test inspector that can be configured to fail.
type mockInspector struct {
	err error
}

func (m *mockInspector) Inspect(raw []byte) (View, error) {
	if m.err != nil {
		return nil, m.err
	}
	return JSONInspector().Inspect(raw)
}

func TestRouter_Hooks(t *testing.T) {
	t.Run("OnParse is called with source and key", func(t *testing.T) {
		var gotSource, gotKey string

		r := New(WithOnParse(func(ctx context.Context, source, key string) context.Context {
			gotSource = source
			gotKey = key
			return ctx
		}))
		r.AddSource(&testSource{name: "mysource"})
		Register(r, "my/event", &testHandler{})

		msg := []byte(`{"type": "my/event", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if gotSource != "mysource" {
			t.Errorf("source = %q, want %q", gotSource, "mysource")
		}
		if gotKey != "my/event" {
			t.Errorf("key = %q, want %q", gotKey, "my/event")
		}
	})

	t.Run("OnDispatch is called before handler", func(t *testing.T) {
		var order []string

		r := New(WithOnDispatch(func(ctx context.Context, source, key string) {
			order = append(order, "dispatch")
		}))
		r.AddSource(&testSource{name: "test"})

		Register(r, "test/event", HandlerFunc[testPayload](func(ctx context.Context, p testPayload) error {
			order = append(order, "handler")
			return nil
		}))

		msg := []byte(`{"type": "test/event", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if len(order) != 2 || order[0] != "dispatch" || order[1] != "handler" {
			t.Errorf("order = %v, want [dispatch handler]", order)
		}
	})

	t.Run("OnSuccess is called on success with duration", func(t *testing.T) {
		var gotDuration time.Duration
		var called bool

		r := New(WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
			called = true
			gotDuration = d
		}))
		r.AddSource(&testSource{name: "test"})
		Register(r, "test/event", &testHandler{})

		msg := []byte(`{"type": "test/event", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if !called {
			t.Error("OnSuccess was not called")
		}
		if gotDuration <= 0 {
			t.Error("duration should be positive")
		}
	})

	t.Run("OnFailure is called on error with duration", func(t *testing.T) {
		wantErr := errors.New("handler error")
		var gotErr error
		var gotDuration time.Duration

		r := New(WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
			gotErr = err
			gotDuration = d
		}))
		r.AddSource(&testSource{name: "test"})
		Register(r, "test/event", &testHandler{err: wantErr})

		msg := []byte(`{"type": "test/event", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if !errors.Is(gotErr, wantErr) {
			t.Errorf("error = %v, want %v", gotErr, wantErr)
		}
		if gotDuration <= 0 {
			t.Error("duration should be positive")
		}
	})

	t.Run("OnNoSource can skip", func(t *testing.T) {
		r := New(WithOnNoSource(func(ctx context.Context, raw []byte) error {
			return nil // skip
		}))
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"not": "matching"}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("OnNoHandler can skip", func(t *testing.T) {
		r := New(WithOnNoHandler(func(ctx context.Context, source, key string) error {
			return nil // skip
		}))
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"type": "unknown", "payload": {}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("OnUnmarshalError can skip", func(t *testing.T) {
		r := New(WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
			return nil // skip
		}))
		r.AddSource(&testSource{name: "test"})

		Register(r, "test/event", &testHandler{})

		// payload is not valid JSON for testPayload
		msg := []byte(`{"type": "test/event", "payload": "not an object"}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestRouter_Completion(t *testing.T) {
	t.Run("Complete is called on success", func(t *testing.T) {
		var completeCalled bool
		var completeErr error

		source := SourceFunc("completion", HasFields("type", "payload"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || env.Type == "" {
				return Parsed{}, false
			}
			return Parsed{
				Key:     env.Type,
				Payload: env.Payload,
				Complete: func(ctx context.Context, err error) error {
					completeCalled = true
					completeErr = err
					return nil
				},
			}, true
		})

		r := New()
		r.AddSource(source)
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {"value": "x"}}`)
		_ = r.Process(context.Background(), msg)

		if !completeCalled {
			t.Error("Complete was not called")
		}
		if completeErr != nil {
			t.Errorf("Complete error = %v, want nil", completeErr)
		}
	})

	t.Run("Complete is called on failure", func(t *testing.T) {
		var completeErr error

		source := SourceFunc("completion", HasFields("type", "payload"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || env.Type == "" {
				return Parsed{}, false
			}
			return Parsed{
				Key:     env.Type,
				Payload: env.Payload,
				Complete: func(ctx context.Context, err error) error {
					completeErr = err
					return nil
				},
			}, true
		})

		r := New()
		r.AddSource(source)

		wantErr := errors.New("handler error")
		Register(r, "test", &testHandler{err: wantErr})

		msg := []byte(`{"type": "test", "payload": {"value": "x"}}`)
		_ = r.Process(context.Background(), msg)

		if !errors.Is(completeErr, wantErr) {
			t.Errorf("Complete error = %v, want %v", completeErr, wantErr)
		}
	})
}

func TestMultipleHooks(t *testing.T) {
	t.Run("chains OnParse contexts", func(t *testing.T) {
		type key string
		var finalCtx context.Context

		r := New(
			WithOnParse(func(ctx context.Context, source, k string) context.Context {
				return context.WithValue(ctx, key("first"), "one")
			}),
			WithOnParse(func(ctx context.Context, source, k string) context.Context {
				return context.WithValue(ctx, key("second"), "two")
			}),
			WithOnParse(func(ctx context.Context, source, k string) context.Context {
				finalCtx = ctx
				return ctx
			}),
		)
		r.AddSource(&testSource{name: "test"})
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if finalCtx.Value(key("first")) != "one" {
			t.Error("first context value not set")
		}
		if finalCtx.Value(key("second")) != "two" {
			t.Error("second context value not set")
		}
	})

	t.Run("calls all OnSuccess hooks", func(t *testing.T) {
		var calls []string

		r := New(
			WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
				calls = append(calls, "first")
			}),
			WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
				calls = append(calls, "second")
			}),
		)
		r.AddSource(&testSource{name: "test"})
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg)

		if len(calls) != 2 || calls[0] != "first" || calls[1] != "second" {
			t.Errorf("calls = %v, want [first second]", calls)
		}
	})

	t.Run("first error wins for OnNoHandler", func(t *testing.T) {
		wantErr := errors.New("first error")

		r := New(
			WithOnNoHandler(func(ctx context.Context, source, key string) error {
				return wantErr
			}),
			WithOnNoHandler(func(ctx context.Context, source, key string) error {
				return errors.New("second error")
			}),
		)
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"type": "unknown", "payload": {}}`)
		err := r.Process(context.Background(), msg)

		if !errors.Is(err, wantErr) {
			t.Errorf("error = %v, want %v", err, wantErr)
		}
	})
}

func TestRegisterFunc(t *testing.T) {
	r := New()
	r.AddSource(&testSource{name: "test"})

	var called bool
	RegisterFunc(r, "test", func(ctx context.Context, p testPayload) error {
		called = true
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": "x"}}`)
	_ = r.Process(context.Background(), msg)

	if !called {
		t.Error("handler func was not called")
	}
}
