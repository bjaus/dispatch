package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
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
	name string
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

// conditionalInspector fails after a configured number of calls.
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

type RouterSuite struct {
	suite.Suite
	router  *Router
	source  *testSource
	handler *testHandler
}

func (s *RouterSuite) SetupTest() {
	s.router = New()
	s.source = &testSource{name: "test"}
	s.handler = &testHandler{}
	s.router.AddSource(s.source)
}

func TestRouterSuite(t *testing.T) {
	suite.Run(t, new(RouterSuite))
}

func (s *RouterSuite) TestProcess_DispatchesToRegisteredHandler() {
	Register(s.router, "test/event", s.handler)

	msg := []byte(`{"type": "test/event", "payload": {"value": "hello"}}`)
	err := s.router.Process(context.Background(), msg)

	s.Require().NoError(err)
	s.Assert().True(s.handler.called)
	s.Assert().Equal("hello", s.handler.payload.Value)
}

func (s *RouterSuite) TestProcess_ReturnsHandlerError() {
	wantErr := errors.New("handler error")
	s.handler.err = wantErr
	Register(s.router, "test/event", s.handler)

	msg := []byte(`{"type": "test/event", "payload": {"value": "hello"}}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().ErrorIs(err, wantErr)
}

func (s *RouterSuite) TestProcess_ReturnsErrorWhenNoSourceMatches() {
	msg := []byte(`{"not": "matching"}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().Error(err)
}

func (s *RouterSuite) TestProcess_ReturnsErrorWhenNoHandlerRegistered() {
	msg := []byte(`{"type": "unknown/event", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().Error(err)
}

func (s *RouterSuite) TestProcess_TriesSourcesInOrder() {
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
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().Equal("second", calledSource)
}

type GroupsSuite struct {
	suite.Suite
	router *Router
}

func (s *GroupsSuite) SetupTest() {
	s.router = New()
}

func TestGroupsSuite(t *testing.T) {
	suite.Run(t, new(GroupsSuite))
}

func (s *GroupsSuite) TestDefaultGroupUsesDefaultInspector() {
	s.router.AddSource(&testSource{name: "json-source"})

	h := &testHandler{}
	Register(s.router, "test/event", h)

	msg := []byte(`{"type": "test/event", "payload": {"value": "hello"}}`)
	err := s.router.Process(context.Background(), msg)

	s.Require().NoError(err)
	s.Assert().True(h.called)
}

func (s *GroupsSuite) TestCustomGroupWithCustomInspector() {
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

	s.router.AddGroup(customInspector, customSource)

	h := &testHandler{}
	Register(s.router, "custom/event", h)

	msg := []byte(`{"event": "custom/event", "data": {"value": "test"}}`)
	err := s.router.Process(context.Background(), msg)

	s.Require().NoError(err)
	s.Assert().True(h.called)
}

func (s *GroupsSuite) TestDefaultGroupCheckedBeforeCustomGroups() {
	var matchedSource string

	// Default group source
	s.router.AddSource(SourceFunc("default", HasFields("type"), func(raw []byte) (Parsed, bool) {
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
	s.router.AddGroup(JSONInspector(), customSource)

	Register(s.router, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().Equal("default", matchedSource)
}

func (s *GroupsSuite) TestWithInspectorOverridesDefaultInspector() {
	failingInspector := &mockInspector{err: ErrInvalidJSON}

	r := New(WithInspector(failingInspector))
	r.AddSource(&testSource{name: "test"})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Assert().Error(err)
}

type AdaptiveOrderingSuite struct {
	suite.Suite
}

func TestAdaptiveOrderingSuite(t *testing.T) {
	suite.Run(t, new(AdaptiveOrderingSuite))
}

func (s *AdaptiveOrderingSuite) TestLastMatchedSourceTriedFirst() {
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
	err := r.Process(context.Background(), msg1)
	s.NoError(err)

	// Clear order tracking
	matchOrder = nil

	// Second message also matches second source - should try it first
	msg2 := []byte(`{"second": true, "type": "test", "payload": {}}`)
	err = r.Process(context.Background(), msg2)

	s.NoError(err)
	s.Require().NotEmpty(matchOrder)
	s.Assert().Equal("second-source", matchOrder[0])
}

func (s *AdaptiveOrderingSuite) TestFallsBackToFullSearchWhenLastMatchFails() {
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
	err := r.Process(context.Background(), msg1)
	s.NoError(err)

	// Now send message that matches first source
	msg2 := []byte(`{"first": true, "type": "test", "payload": {}}`)
	err = r.Process(context.Background(), msg2)

	s.Assert().NoError(err)
}

type HooksSuite struct {
	suite.Suite
	router  *Router
	source  *testSource
	handler *testHandler
}

func (s *HooksSuite) SetupTest() {
	s.source = &testSource{name: "test"}
	s.handler = &testHandler{}
}

func TestHooksSuite(t *testing.T) {
	suite.Run(t, new(HooksSuite))
}

func (s *HooksSuite) TestOnParseCalledWithSourceAndKey() {
	var gotSource, gotKey string

	s.router = New(WithOnParse(func(ctx context.Context, source, key string) context.Context {
		gotSource = source
		gotKey = key
		return ctx
	}))
	s.router.AddSource(&testSource{name: "mysource"})
	Register(s.router, "my/event", &testHandler{})

	msg := []byte(`{"type": "my/event", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().Equal("mysource", gotSource)
	s.Assert().Equal("my/event", gotKey)
}

func (s *HooksSuite) TestOnDispatchCalledBeforeHandler() {
	var order []string

	s.router = New(WithOnDispatch(func(ctx context.Context, source, key string) {
		order = append(order, "dispatch")
	}))
	s.router.AddSource(s.source)

	Register(s.router, "test/event", HandlerFunc[testPayload](func(ctx context.Context, p testPayload) error {
		order = append(order, "handler")
		return nil
	}))

	msg := []byte(`{"type": "test/event", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.NoError(err)
	s.Require().Len(order, 2)
	s.Assert().Equal("dispatch", order[0])
	s.Assert().Equal("handler", order[1])
}

func (s *HooksSuite) TestOnSuccessCalledWithDuration() {
	var gotDuration time.Duration
	var called bool

	s.router = New(WithOnSuccess(func(ctx context.Context, source, key string, d time.Duration) {
		called = true
		gotDuration = d
	}))
	s.router.AddSource(s.source)
	Register(s.router, "test/event", s.handler)

	msg := []byte(`{"type": "test/event", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().True(called)
	s.Assert().Greater(gotDuration, time.Duration(0))
}

func (s *HooksSuite) TestOnFailureCalledWithErrorAndDuration() {
	wantErr := errors.New("handler error")
	var gotErr error
	var gotDuration time.Duration

	s.router = New(WithOnFailure(func(ctx context.Context, source, key string, err error, d time.Duration) {
		gotErr = err
		gotDuration = d
	}))
	s.router.AddSource(s.source)
	Register(s.router, "test/event", &testHandler{err: wantErr})

	msg := []byte(`{"type": "test/event", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.ErrorIs(err, wantErr)
	s.Assert().ErrorIs(gotErr, wantErr)
	s.Assert().Greater(gotDuration, time.Duration(0))
}

func (s *HooksSuite) TestOnNoSourceCanSkip() {
	s.router = New(WithOnNoSource(func(ctx context.Context, raw []byte) error {
		return nil
	}))
	s.router.AddSource(s.source)

	msg := []byte(`{"not": "matching"}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().NoError(err)
}

func (s *HooksSuite) TestOnNoHandlerCanSkip() {
	s.router = New(WithOnNoHandler(func(ctx context.Context, source, key string) error {
		return nil
	}))
	s.router.AddSource(s.source)

	msg := []byte(`{"type": "unknown", "payload": {}}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().NoError(err)
}

func (s *HooksSuite) TestOnUnmarshalErrorCanSkip() {
	s.router = New(WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
		return nil
	}))
	s.router.AddSource(s.source)

	Register(s.router, "test/event", s.handler)

	msg := []byte(`{"type": "test/event", "payload": "not an object"}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().NoError(err)
}

type CompletionSuite struct {
	suite.Suite
}

func TestCompletionSuite(t *testing.T) {
	suite.Run(t, new(CompletionSuite))
}

func (s *CompletionSuite) makeSourceWithCompletion(completeCalled *bool, completeErr *error) Source {
	return SourceFunc("completion", HasFields("type", "payload"), func(raw []byte) (Parsed, bool) {
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
				*completeCalled = true
				*completeErr = err
				return nil
			},
		}, true
	})
}

func (s *CompletionSuite) TestCompleteCalledOnSuccess() {
	var completeCalled bool
	var completeErr error

	r := New()
	r.AddSource(s.makeSourceWithCompletion(&completeCalled, &completeErr))
	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {"value": "x"}}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().True(completeCalled)
	s.Assert().NoError(completeErr)
}

func (s *CompletionSuite) TestCompleteCalledOnFailure() {
	var completeCalled bool
	var completeErr error

	r := New()
	r.AddSource(s.makeSourceWithCompletion(&completeCalled, &completeErr))

	wantErr := errors.New("handler error")
	Register(r, "test", &testHandler{err: wantErr})

	msg := []byte(`{"type": "test", "payload": {"value": "x"}}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err) // Complete callback returns nil, swallowing the handler error
	s.Assert().True(completeCalled)
	s.Assert().ErrorIs(completeErr, wantErr)
}

type MultipleHooksSuite struct {
	suite.Suite
}

func TestMultipleHooksSuite(t *testing.T) {
	suite.Run(t, new(MultipleHooksSuite))
}

func (s *MultipleHooksSuite) TestChainsOnParseContexts() {
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
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().Equal("one", finalCtx.Value(key("first")))
	s.Assert().Equal("two", finalCtx.Value(key("second")))
}

func (s *MultipleHooksSuite) TestCallsAllOnSuccessHooks() {
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
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Require().Len(calls, 2)
	s.Assert().Equal("first", calls[0])
	s.Assert().Equal("second", calls[1])
}

func (s *MultipleHooksSuite) TestFirstErrorWinsForOnNoHandler() {
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

	s.Assert().ErrorIs(err, wantErr)
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
	err := r.Process(context.Background(), msg)

	require.NoError(t, err)
	assert.True(t, called)
}

type ValidationSuite struct {
	suite.Suite
	router *Router
	source *testSource
}

func (s *ValidationSuite) SetupTest() {
	s.router = New()
	s.source = &testSource{name: "test"}
	s.router.AddSource(s.source)
}

func TestValidationSuite(t *testing.T) {
	suite.Run(t, new(ValidationSuite))
}

func (s *ValidationSuite) TestValidatesPayloadWhenValidatable() {
	RegisterFunc(s.router, "test", func(ctx context.Context, p validatablePayload) error {
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
	err := s.router.Process(context.Background(), msg)

	s.Assert().Error(err)
}

func (s *ValidationSuite) TestValidPayloadPassesValidation() {
	var called bool
	RegisterFunc(s.router, "test", func(ctx context.Context, p validatablePayload) error {
		called = true
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": "valid"}}`)
	err := s.router.Process(context.Background(), msg)

	s.Require().NoError(err)
	s.Assert().True(called)
}

func (s *ValidationSuite) TestOnValidationErrorCanSkip() {
	var hookCalled bool
	var hookErr error

	r := New(WithOnValidationError(func(ctx context.Context, source, key string, err error) error {
		hookCalled = true
		hookErr = err
		return nil
	}))
	r.AddSource(s.source)

	RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
		s.Fail("handler should not be called on validation error")
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
	err := r.Process(context.Background(), msg)

	s.Assert().NoError(err)
	s.Assert().True(hookCalled)
	s.Assert().Error(hookErr)
}

func (s *ValidationSuite) TestOnValidationErrorCanReturnCustomError() {
	customErr := errors.New("custom validation error")

	r := New(WithOnValidationError(func(ctx context.Context, source, key string, err error) error {
		return customErr
	}))
	r.AddSource(s.source)

	RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
	err := r.Process(context.Background(), msg)

	s.Assert().ErrorIs(err, customErr)
}

func (s *ValidationSuite) TestValidationErrorWithCompletionCallback() {
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
	err := r.Process(context.Background(), msg)

	s.NoError(err) // Complete callback returns nil, swallowing the validation error
	s.Assert().True(completeCalled)
	s.Assert().Error(completeErr)
}

func (s *ValidationSuite) TestSourceOnValidationErrorHookCalled() {
	source := &sourceWithHooks{name: "test"}

	r := New()
	r.AddSource(source)

	RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
	err := r.Process(context.Background(), msg)

	s.Error(err)
	s.Assert().True(source.onValidationErrorCalled)
}

type TrySourceInGroupsSuite struct {
	suite.Suite
}

func TestTrySourceInGroupsSuite(t *testing.T) {
	suite.Run(t, new(TrySourceInGroupsSuite))
}

func (s *TrySourceInGroupsSuite) TestAdaptiveOrderingWorksWithCustomGroups() {
	r := New()

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

	msg1 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg1)
	s.Require().NoError(err)

	msg2 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
	err = r.Process(context.Background(), msg2)
	s.Assert().NoError(err)
}

func (s *TrySourceInGroupsSuite) TestTrySourceHandlesInspectorErrorInCustomGroups() {
	r := New()

	r.AddSource(&testSource{name: "default"})

	failingInspector := &mockInspector{err: ErrInvalidJSON}
	customSource := SourceFunc("custom", HasFields("custom"), func(raw []byte) (Parsed, bool) {
		return Parsed{Key: "test", Payload: []byte(`{}`)}, true
	})
	r.AddGroup(failingInspector, customSource)

	Register(r, "test/event", &testHandler{})

	msg1 := []byte(`{"type": "test/event", "payload": {}}`)
	err := r.Process(context.Background(), msg1)
	s.NoError(err)

	msg2 := []byte(`{"type": "test/event", "payload": {}}`)
	err = r.Process(context.Background(), msg2)

	s.Assert().NoError(err)
}

type UnmarshalErrorSuite struct {
	suite.Suite
}

func TestUnmarshalErrorSuite(t *testing.T) {
	suite.Run(t, new(UnmarshalErrorSuite))
}

func (s *UnmarshalErrorSuite) TestUnmarshalErrorCallsCompletionCallback() {
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

	msg := []byte(`{"type": "test", "payload": "not an object"}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err) // Complete callback returns nil, swallowing the unmarshal error
	s.Assert().True(completeCalled)
	s.Assert().Error(completeErr)
}

type ErrorTypesSuite struct {
	suite.Suite
}

func TestErrorTypesSuite(t *testing.T) {
	suite.Run(t, new(ErrorTypesSuite))
}

func (s *ErrorTypesSuite) TestUnmarshalErrorImplementsErrorInterface() {
	inner := errors.New("json parse error")
	err := &unmarshalError{err: inner}

	s.Assert().Equal(inner.Error(), err.Error())
	s.Assert().Equal(inner, err.Unwrap())
}

func (s *ErrorTypesSuite) TestValidationErrorImplementsErrorInterface() {
	inner := errors.New("field required")
	err := &validationError{err: inner}

	s.Assert().Equal(inner.Error(), err.Error())
	s.Assert().Equal(inner, err.Unwrap())
}

func TestRouter_HandleNoSourceWithError(t *testing.T) {
	wantErr := errors.New("no source error")

	r := New(WithOnNoSource(func(ctx context.Context, raw []byte) error {
		return wantErr
	}))
	r.AddSource(&testSource{name: "test"})

	msg := []byte(`{"not": "matching"}`)
	err := r.Process(context.Background(), msg)

	assert.ErrorIs(t, err, wantErr)
}

func TestRouter_OnUnmarshalErrorReturnsCustomError(t *testing.T) {
	customErr := errors.New("custom unmarshal error")

	r := New(WithOnUnmarshalError(func(ctx context.Context, source, key string, err error) error {
		return customErr
	}))
	r.AddSource(&testSource{name: "test"})
	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": "invalid"}`)
	err := r.Process(context.Background(), msg)

	assert.ErrorIs(t, err, customErr)
}

func TestRouter_SourceParseFailsAfterDiscriminatorMatch(t *testing.T) {
	flakySource := SourceFunc("flaky", HasFields("type"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	})

	r := New()
	r.AddSource(flakySource)

	msg := []byte(`{"type": "test"}`)
	err := r.Process(context.Background(), msg)

	assert.Error(t, err)
}

func TestRouter_SourceValidationErrorHookReturnsError(t *testing.T) {
	sourceErr := errors.New("source validation error")
	source := &sourceWithHooks{
		name:                 "test",
		onValidationErrorErr: sourceErr,
	}

	r := New(WithOnValidationError(func(ctx context.Context, src, key string, err error) error {
		return nil
	}))
	r.AddSource(source)

	RegisterFunc(r, "test", func(ctx context.Context, p validatablePayload) error {
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {"value": ""}}`)
	err := r.Process(context.Background(), msg)

	assert.ErrorIs(t, err, sourceErr)
}

func TestRouter_CustomGroupMatchAll(t *testing.T) {
	r := New()

	r.AddSource(SourceFunc("default", HasFields("default_field"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	}))

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

	require.NoError(t, err)
	assert.True(t, called)
}

func TestRouter_TrySourceCustomGroupMatch(t *testing.T) {
	r := New()

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

	msg1 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg1)
	require.NoError(t, err)

	msg2 := []byte(`{"custom": true, "type": "test", "payload": {}}`)
	err = r.Process(context.Background(), msg2)

	assert.NoError(t, err)
}

func TestRouter_MatchAllCustomGroupInspectorError(t *testing.T) {
	r := New()

	failingInspector := &mockInspector{err: ErrInvalidJSON}
	customSource := SourceFunc("custom", HasFields("custom"), func(raw []byte) (Parsed, bool) {
		return Parsed{Key: "test", Payload: []byte(`{}`)}, true
	})
	r.AddGroup(failingInspector, customSource)

	msg := []byte(`{"custom": true, "type": "test"}`)
	err := r.Process(context.Background(), msg)

	assert.Error(t, err)
}

func TestRouter_TrySourceFindsInCustomGroupDirectly(t *testing.T) {
	r := New()

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

	msg1 := []byte(`{"x": true, "type": "event", "payload": {}}`)
	err := r.Process(context.Background(), msg1)
	require.NoError(t, err)

	msg2 := []byte(`{"x": true, "type": "event", "payload": {}}`)
	err = r.Process(context.Background(), msg2)
	assert.NoError(t, err)
}

func TestRouter_TrySourceInspectorFailsInCustomGroup(t *testing.T) {
	r := New()

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

	msg1 := []byte(`{"c": true, "type": "event", "payload": {}}`)
	err := r.Process(context.Background(), msg1)
	require.NoError(t, err)

	msg2 := []byte(`{"c": true, "type": "event", "payload": {}}`)
	err = r.Process(context.Background(), msg2)

	assert.Error(t, err)
}

// countingInspector tracks how many times Inspect is called.
type countingInspector struct {
	count int
}

func (c *countingInspector) Inspect(raw []byte) (View, error) {
	c.count++
	return JSONInspector().Inspect(raw)
}

func (c *countingInspector) reset() {
	c.count = 0
}

type ViewCachingSuite struct {
	suite.Suite
}

func TestViewCachingSuite(t *testing.T) {
	suite.Run(t, new(ViewCachingSuite))
}

func (s *ViewCachingSuite) TestInspectorCalledOnceWhenTrySourceSucceeds() {
	inspector := &countingInspector{}

	r := New(WithInspector(inspector))

	source := SourceFunc("test-source", HasFields("type"), func(raw []byte) (Parsed, bool) {
		var env struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || env.Type == "" {
			return Parsed{}, false
		}
		return Parsed{Key: env.Type, Payload: env.Payload}, true
	})
	r.AddSource(source)
	Register(r, "test", &testHandler{})

	// First message - no lastMatch, goes through matchAll
	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)
	s.Require().NoError(err)

	// Inspector should be called exactly once
	s.Assert().Equal(1, inspector.count)
}

func (s *ViewCachingSuite) TestInspectorCalledOnceWhenTrySourceFailsAndMatchAllSucceeds() {
	inspector := &countingInspector{}

	r := New(WithInspector(inspector))

	// Source A - matches "a" field
	sourceA := SourceFunc("source-a", HasFields("a"), func(raw []byte) (Parsed, bool) {
		var env struct {
			A       bool            `json:"a"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || !env.A {
			return Parsed{}, false
		}
		return Parsed{Key: env.Type, Payload: env.Payload}, true
	})

	// Source B - matches "b" field
	sourceB := SourceFunc("source-b", HasFields("b"), func(raw []byte) (Parsed, bool) {
		var env struct {
			B       bool            `json:"b"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || !env.B {
			return Parsed{}, false
		}
		return Parsed{Key: env.Type, Payload: env.Payload}, true
	})

	r.AddSource(sourceA)
	r.AddSource(sourceB)
	Register(r, "test", &testHandler{})

	// Prime with source-a
	msg1 := []byte(`{"a": true, "type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg1)
	s.Require().NoError(err)
	inspector.reset()

	// Now send message that matches source-b
	// trySource will try "source-a" first (lastMatch), fail
	// matchAll will find "source-b"
	// With caching, inspector should only be called ONCE
	msg2 := []byte(`{"b": true, "type": "test", "payload": {}}`)
	err = r.Process(context.Background(), msg2)

	s.Require().NoError(err)
	s.Assert().Equal(1, inspector.count, "inspector should only be called once due to view caching")
}

func (s *ViewCachingSuite) TestInspectorCalledOncePerGroupWithMultipleGroups() {
	defaultInspector := &countingInspector{}
	group1Inspector := &countingInspector{}
	group2Inspector := &countingInspector{}

	r := New(WithInspector(defaultInspector))

	// Default source - won't match
	defaultSource := SourceFunc("default", HasFields("default_field"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	})
	r.AddSource(defaultSource)

	// Group 1 source - won't match
	group1Source := SourceFunc("group1", HasFields("group1_field"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	})
	r.AddGroup(group1Inspector, group1Source)

	// Group 2 source - will match
	group2Source := SourceFunc("group2", HasFields("group2_field"), func(raw []byte) (Parsed, bool) {
		var env struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil {
			return Parsed{}, false
		}
		return Parsed{Key: env.Type, Payload: env.Payload}, true
	})
	r.AddGroup(group2Inspector, group2Source)

	Register(r, "test", &testHandler{})

	msg := []byte(`{"group2_field": true, "type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Require().NoError(err)
	s.Assert().Equal(1, defaultInspector.count, "default inspector should be called once")
	s.Assert().Equal(1, group1Inspector.count, "group1 inspector should be called once")
	s.Assert().Equal(1, group2Inspector.count, "group2 inspector should be called once")
}

func (s *ViewCachingSuite) TestSameInspectorSharedAcrossGroupsCalledOnce() {
	sharedInspector := &countingInspector{}

	r := New(WithInspector(sharedInspector))

	// Default source - won't match
	defaultSource := SourceFunc("default", HasFields("default_field"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	})
	r.AddSource(defaultSource)

	// Custom group using the SAME inspector
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
	r.AddGroup(sharedInspector, customSource)

	Register(r, "test", &testHandler{})

	msg := []byte(`{"custom_field": true, "type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Require().NoError(err)
	// Same inspector used for default group and custom group - should only be called once
	s.Assert().Equal(1, sharedInspector.count, "shared inspector should only be called once")
}

func (s *ViewCachingSuite) TestViewCacheHandlesInspectorError() {
	failingInspector := &mockInspector{err: ErrInvalidJSON}
	workingInspector := &countingInspector{}

	r := New(WithInspector(failingInspector))

	// Default source with failing inspector
	defaultSource := SourceFunc("default", HasFields("x"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	})
	r.AddSource(defaultSource)

	// Custom group with working inspector
	customSource := SourceFunc("custom", HasFields("type"), func(raw []byte) (Parsed, bool) {
		var env struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil {
			return Parsed{}, false
		}
		return Parsed{Key: env.Type, Payload: env.Payload}, true
	})
	r.AddGroup(workingInspector, customSource)

	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Require().NoError(err)
	s.Assert().Equal(1, workingInspector.count)
}

func (s *ViewCachingSuite) TestViewCacheCachesFailureResult() {
	// Inspector that fails but counts calls
	failingInspector := &failingCountingInspector{}
	workingInspector := &countingInspector{}

	r := New(WithInspector(failingInspector))

	// Add multiple sources to default group to force multiple discriminator checks
	r.AddSource(SourceFunc("src1", HasFields("a"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	}))
	r.AddSource(SourceFunc("src2", HasFields("b"), func(raw []byte) (Parsed, bool) {
		return Parsed{}, false
	}))

	// Custom group with working inspector
	customSource := SourceFunc("custom", HasFields("type"), func(raw []byte) (Parsed, bool) {
		var env struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil {
			return Parsed{}, false
		}
		return Parsed{Key: env.Type, Payload: env.Payload}, true
	})
	r.AddGroup(workingInspector, customSource)

	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Require().NoError(err)
	// Failing inspector should only be called once, even with multiple sources
	s.Assert().Equal(1, failingInspector.count, "failing inspector should only be called once (result cached)")
	s.Assert().Equal(1, workingInspector.count)
}

// failingCountingInspector always fails but counts calls.
type failingCountingInspector struct {
	count int
}

func (f *failingCountingInspector) Inspect(raw []byte) (View, error) {
	f.count++
	return nil, ErrInvalidJSON
}
