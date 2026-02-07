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

func TestRouter_AdaptiveOrdering(t *testing.T) {
	t.Run("last matched source is tried first", func(t *testing.T) {
		var matchOrder []string

		r := New()

		// First source - matches "first" type
		r.AddSource(SourceFunc("first-source", HasFields("first"), func(raw []byte) (Parsed, bool) {
			matchOrder = append(matchOrder, "first-source")
			var env struct {
				First   bool            `json:"first"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || !env.First {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		}))

		// Second source - matches "second" type
		r.AddSource(SourceFunc("second-source", HasFields("second"), func(raw []byte) (Parsed, bool) {
			matchOrder = append(matchOrder, "second-source")
			var env struct {
				Second  bool            `json:"second"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || !env.Second {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		}))

		Register(r, "test", &testHandler{})

		// First message matches second source
		msg1 := []byte(`{"second": true, "type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg1)

		// Clear order tracking
		matchOrder = nil

		// Second message also matches second source - should try it first
		msg2 := []byte(`{"second": true, "type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg2)

		// Second source should be tried first due to adaptive ordering
		if len(matchOrder) == 0 {
			t.Fatal("no sources were tried")
		}
		if matchOrder[0] != "second-source" {
			t.Errorf("first tried source = %q, want %q", matchOrder[0], "second-source")
		}
	})

	t.Run("falls back to full search when last match fails", func(t *testing.T) {
		r := New()

		r.AddSource(SourceFunc("first-source", HasFields("first"), func(raw []byte) (Parsed, bool) {
			var env struct {
				First   bool            `json:"first"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || !env.First {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		}))

		r.AddSource(SourceFunc("second-source", HasFields("second"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Second  bool            `json:"second"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || !env.Second {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		}))

		Register(r, "test", &testHandler{})

		// Prime with second source
		msg1 := []byte(`{"second": true, "type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg1)

		// Now send message that matches first source
		msg2 := []byte(`{"first": true, "type": "test", "payload": {}}`)
		err := r.Process(context.Background(), msg2)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
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

// validatablePayload implements the validatable interface for testing.
type validatablePayload struct {
	Value string `json:"value"`
}

func (p *validatablePayload) Validate() error {
	if p.Value == "" {
		return errors.New("value is required")
	}
	return nil
}

func TestRouter_Validation(t *testing.T) {
	t.Run("validates payload when validatable", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "test"})

		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			return nil
		})

		// Empty value should fail validation
		msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
		err := r.Process(context.Background(), msg)

		if err == nil {
			t.Error("expected validation error")
		}
	})

	t.Run("valid payload passes validation", func(t *testing.T) {
		r := New()
		r.AddSource(&testSource{name: "test"})

		var called bool
		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			called = true
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {"value": "valid"}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !called {
			t.Error("handler was not called")
		}
	})

	t.Run("OnValidationError can skip", func(t *testing.T) {
		var hookCalled bool
		var hookErr error

		r := New(WithOnValidationError(func(ctx context.Context, source, key string, err error) error {
			hookCalled = true
			hookErr = err
			return nil // skip
		}))
		r.AddSource(&testSource{name: "test"})

		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			t.Error("handler should not be called on validation error")
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("expected nil error after skip, got: %v", err)
		}
		if !hookCalled {
			t.Error("OnValidationError hook was not called")
		}
		if hookErr == nil {
			t.Error("hook should receive the validation error")
		}
	})

	t.Run("OnValidationError can return custom error", func(t *testing.T) {
		customErr := errors.New("custom validation error")

		r := New(WithOnValidationError(func(ctx context.Context, source, key string, err error) error {
			return customErr
		}))
		r.AddSource(&testSource{name: "test"})

		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
		err := r.Process(context.Background(), msg)

		if !errors.Is(err, customErr) {
			t.Errorf("error = %v, want %v", err, customErr)
		}
	})

	t.Run("validation error with completion callback", func(t *testing.T) {
		var completeErr error
		var completeCalled bool

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

		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
		_ = r.Process(context.Background(), msg)

		if !completeCalled {
			t.Error("Complete was not called")
		}
		if completeErr == nil {
			t.Error("Complete should receive the validation error")
		}
	})

	t.Run("source OnValidationError hook is called", func(t *testing.T) {
		source := &sourceWithHooks{name: "test"}

		r := New()
		r.AddSource(source)

		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
		_ = r.Process(context.Background(), msg)

		if !source.onValidationErrorCalled {
			t.Error("source OnValidationError hook was not called")
		}
	})
}

func TestRouter_TrySourceInGroups(t *testing.T) {
	t.Run("adaptive ordering works with custom groups", func(t *testing.T) {
		r := New()

		// Add a source to a custom group
		customSource := SourceFunc("custom-group-source", HasFields("custom"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Custom  bool            `json:"custom"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || !env.Custom {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})

		r.AddGroup(JSONInspector(), customSource)
		Register(r, "test", &testHandler{})

		// First message primes the adaptive ordering
		msg1 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
		err := r.Process(context.Background(), msg1)
		if err != nil {
			t.Fatalf("first message failed: %v", err)
		}

		// Second message should use adaptive ordering to find the source in custom group
		msg2 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
		err = r.Process(context.Background(), msg2)
		if err != nil {
			t.Errorf("second message failed: %v", err)
		}
	})

	t.Run("trySource handles inspector error in custom groups", func(t *testing.T) {
		r := New()

		// Add source to default group first
		r.AddSource(&testSource{name: "default"})

		// Add source to custom group with failing inspector
		failingInspector := &mockInspector{err: ErrInvalidJSON}
		customSource := SourceFunc("custom", HasFields("custom"), func(raw []byte) (Parsed, bool) {
			return Parsed{Key: "test", Payload: []byte(`{}`)}, true
		})
		r.AddGroup(failingInspector, customSource)

		Register(r, "test/event", &testHandler{})

		// Prime with default source
		msg1 := []byte(`{"type": "test/event", "payload": {}}`)
		_ = r.Process(context.Background(), msg1)

		// This should fall back correctly even with failing custom group inspector
		msg2 := []byte(`{"type": "test/event", "payload": {}}`)
		err := r.Process(context.Background(), msg2)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestRouter_UnmarshalErrorWithCompletion(t *testing.T) {
	t.Run("unmarshal error calls completion callback", func(t *testing.T) {
		var completeErr error
		var completeCalled bool

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

		// Invalid payload that can't unmarshal to testPayload
		msg := []byte(`{"type": "test", "payload": "not an object"}`)
		_ = r.Process(context.Background(), msg)

		if !completeCalled {
			t.Error("Complete was not called")
		}
		if completeErr == nil {
			t.Error("Complete should receive the unmarshal error")
		}
	})
}

func TestErrorTypes(t *testing.T) {
	t.Run("unmarshalError implements error interface", func(t *testing.T) {
		inner := errors.New("json parse error")
		err := &unmarshalError{err: inner}

		if err.Error() != inner.Error() {
			t.Errorf("Error() = %q, want %q", err.Error(), inner.Error())
		}
		if err.Unwrap() != inner {
			t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
		}
	})

	t.Run("validationError implements error interface", func(t *testing.T) {
		inner := errors.New("field required")
		err := &validationError{err: inner}

		if err.Error() != inner.Error() {
			t.Errorf("Error() = %q, want %q", err.Error(), inner.Error())
		}
		if err.Unwrap() != inner {
			t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
		}
	})
}

func TestRouter_HandleNoSourceWithError(t *testing.T) {
	t.Run("OnNoSource hook can return error", func(t *testing.T) {
		wantErr := errors.New("no source error")

		r := New(WithOnNoSource(func(ctx context.Context, raw []byte) error {
			return wantErr
		}))
		r.AddSource(&testSource{name: "test"})

		msg := []byte(`{"not": "matching"}`)
		err := r.Process(context.Background(), msg)

		if !errors.Is(err, wantErr) {
			t.Errorf("error = %v, want %v", err, wantErr)
		}
	})
}

func TestRouter_OnUnmarshalErrorReturnsCustomError(t *testing.T) {
	t.Run("OnUnmarshalError can return custom error", func(t *testing.T) {
		customErr := errors.New("custom unmarshal error")

		r := New(WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
			return customErr
		}))
		r.AddSource(&testSource{name: "test"})
		Register(r, "test", &testHandler{})

		msg := []byte(`{"type": "test", "payload": "invalid"}`)
		err := r.Process(context.Background(), msg)

		if !errors.Is(err, customErr) {
			t.Errorf("error = %v, want %v", err, customErr)
		}
	})
}

func TestRouter_SourceParseFailsAfterDiscriminatorMatch(t *testing.T) {
	t.Run("discriminator matches but parse fails", func(t *testing.T) {
		// Source where discriminator matches but Parse returns false
		flakySource := SourceFunc("flaky", HasFields("type"), func(raw []byte) (Parsed, bool) {
			// Always fail to parse
			return Parsed{}, false
		})

		r := New()
		r.AddSource(flakySource)

		msg := []byte(`{"type": "test"}`)
		err := r.Process(context.Background(), msg)

		if err == nil {
			t.Error("expected error when no source can parse")
		}
	})
}

func TestRouter_SourceValidationErrorHookReturnsError(t *testing.T) {
	t.Run("source OnValidationError can return error when global skips", func(t *testing.T) {
		sourceErr := errors.New("source validation error")
		source := &sourceWithHooks{
			name:                 "test",
			onValidationErrorErr: sourceErr,
		}

		r := New(WithOnValidationError(func(ctx context.Context, src, key string, err error) error {
			return nil // global skips
		}))
		r.AddSource(source)

		RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
			return nil
		})

		msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
		err := r.Process(context.Background(), msg)

		if !errors.Is(err, sourceErr) {
			t.Errorf("error = %v, want %v", err, sourceErr)
		}
	})
}

func TestRouter_CustomGroupMatchAll(t *testing.T) {
	t.Run("matchAll finds source in custom group when default fails", func(t *testing.T) {
		r := New()

		// Add default source that won't match
		r.AddSource(SourceFunc("default", HasFields("default_field"), func(raw []byte) (Parsed, bool) {
			return Parsed{}, false
		}))

		// Add custom group source that will match
		customSource := SourceFunc("custom", HasFields("custom_field"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})
		r.AddGroup(JSONInspector(), customSource)

		var called bool
		RegisterFunc(r, "test", func(ctx context.Context, p testPayload) error {
			called = true
			return nil
		})

		msg := []byte(`{"custom_field": true, "type": "test", "payload": {"value": "x"}}`)
		err := r.Process(context.Background(), msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !called {
			t.Error("handler was not called")
		}
	})
}

func TestRouter_TrySourceCustomGroupMatch(t *testing.T) {
	t.Run("trySource finds source in custom group via adaptive ordering", func(t *testing.T) {
		r := New()

		// Custom group source
		customSource := SourceFunc("custom-src", HasFields("custom"), func(raw []byte) (Parsed, bool) {
			var env struct {
				Custom  bool            `json:"custom"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || !env.Custom {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})
		r.AddGroup(JSONInspector(), customSource)

		Register(r, "test", &testHandler{})

		// Prime adaptive ordering with custom source
		msg1 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
		_ = r.Process(context.Background(), msg1)

		// Now lastMatch should be "custom-src"
		// Second call should try custom-src first via trySource
		msg2 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
		err := r.Process(context.Background(), msg2)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestRouter_MatchAllCustomGroupInspectorError(t *testing.T) {
	t.Run("matchAll handles inspector error in custom group", func(t *testing.T) {
		r := New()

		// Custom group with failing inspector
		failingInspector := &mockInspector{err: ErrInvalidJSON}
		customSource := SourceFunc("custom", HasFields("custom"), func(raw []byte) (Parsed, bool) {
			return Parsed{Key: "test", Payload: []byte(`{}`)}, true
		})
		r.AddGroup(failingInspector, customSource)

		msg := []byte(`{"custom": true, "type": "test"}`)
		err := r.Process(context.Background(), msg)

		// Should get "no source matched" error since inspector fails
		if err == nil {
			t.Error("expected error when inspector fails")
		}
	})
}

func TestRouter_TrySourceFindsInCustomGroupDirectly(t *testing.T) {
	t.Run("adaptive ordering finds source in custom group on second call", func(t *testing.T) {
		r := New()

		// No default sources - only custom group
		customSource := SourceFunc("only-custom", HasFields("x"), func(raw []byte) (Parsed, bool) {
			var env struct {
				X       bool            `json:"x"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || !env.X {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})
		r.AddGroup(JSONInspector(), customSource)

		Register(r, "event", &testHandler{})

		// First call - matchAll finds source in custom group, stores "only-custom" in lastMatch
		msg1 := []byte(`{"x": true, "type": "event", "payload": {}}`)
		err := r.Process(context.Background(), msg1)
		if err != nil {
			t.Fatalf("first call failed: %v", err)
		}

		// Second call - trySource should look for "only-custom" in custom groups
		// Since there are no default sources, trySource will skip to custom groups
		msg2 := []byte(`{"x": true, "type": "event", "payload": {}}`)
		err = r.Process(context.Background(), msg2)
		if err != nil {
			t.Errorf("second call failed: %v", err)
		}
	})
}

// conditionalInspector fails on the second call
type conditionalInspector struct {
	callCount int
	failAfter int
}

func (c *conditionalInspector) Inspect(raw []byte) (View, error) {
	c.callCount++
	if c.callCount > c.failAfter {
		return nil, ErrInvalidJSON
	}
	return JSONInspector().Inspect(raw)
}

func TestRouter_TrySourceInspectorFailsInCustomGroup(t *testing.T) {
	t.Run("trySource continues when custom group inspector fails", func(t *testing.T) {
		r := New()

		// Inspector that works first time (matchAll), fails on second (trySource)
		conditionalInsp := &conditionalInspector{failAfter: 1}

		customSource := SourceFunc("conditional-src", HasFields("c"), func(raw []byte) (Parsed, bool) {
			var env struct {
				C       bool            `json:"c"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || !env.C {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})
		r.AddGroup(conditionalInsp, customSource)

		// Add default source that won't match the first message (no "c" field check)
		defaultSource := SourceFunc("default-src", HasFields("d"), func(raw []byte) (Parsed, bool) {
			var env struct {
				D       bool            `json:"d"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &env) != nil || !env.D {
				return Parsed{}, false
			}
			return Parsed{Key: env.Type, Payload: env.Payload}, true
		})
		r.AddSource(defaultSource)

		Register(r, "event", &testHandler{})

		// First call - default source doesn't match (no "d" field), custom group matches
		// This sets lastMatch to "conditional-src"
		msg1 := []byte(`{"c": true, "type": "event", "payload": {}}`)
		err := r.Process(context.Background(), msg1)
		if err != nil {
			t.Fatalf("first call failed: %v", err)
		}

		// Second call - trySource looks for "conditional-src"
		// - Checks default sources (none named "conditional-src")
		// - Checks custom group, inspector fails (this is the line we want!)
		// - Falls back to matchAll
		// - matchAll also fails because inspector still broken
		// Should get "no source matched" error
		msg2 := []byte(`{"c": true, "type": "event", "payload": {}}`)
		err = r.Process(context.Background(), msg2)

		// We expect an error because the inspector fails
		if err == nil {
			t.Error("expected error when inspector fails")
		}
	})
}
