package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type contextKey string

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

func (s *sourceWithHooks) Parse(raw []byte) (Parsed, error) {
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Parsed{}, err
	}
	if env.Type == "" {
		return Parsed{}, errors.New("missing type")
	}
	return Parsed{Key: env.Type, Payload: env.Payload}, nil
}

func (s *sourceWithHooks) OnParse(ctx context.Context, key string) context.Context {
	s.onParseCalled = true
	return context.WithValue(ctx, contextKey("source-hook"), "called")
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

type SourceHooksSuite struct {
	suite.Suite
}

func TestSourceHooksSuite(t *testing.T) {
	suite.Run(t, new(SourceHooksSuite))
}

func (s *SourceHooksSuite) TestOnParseCalledAfterGlobal() {
	var order []string

	source := &sourceWithHooks{name: "test"}

	r := New(WithOnParse(func(ctx context.Context, src, key string) context.Context {
		order = append(order, "global")
		return ctx
	}))
	r.AddSource(source)
	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().True(source.onParseCalled)
	s.Require().Len(order, 1)
	s.Assert().Equal("global", order[0])
}

func (s *SourceHooksSuite) TestOnDispatchCalledAfterGlobal() {
	var order []string

	source := &sourceWithHooks{name: "test"}

	r := New(WithOnDispatch(func(ctx context.Context, src, key string) {
		order = append(order, "global")
	}))
	r.AddSource(source)
	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().True(source.onDispatchCalled)
	s.Require().Len(order, 1)
	s.Assert().Equal("global", order[0])
}

func (s *SourceHooksSuite) TestOnSuccessCalledAfterGlobal() {
	var order []string

	source := &sourceWithHooks{name: "test"}

	r := New(WithOnSuccess(func(ctx context.Context, src, key string, d time.Duration) {
		order = append(order, "global")
	}))
	r.AddSource(source)
	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().True(source.onSuccessCalled)
	s.Require().Len(order, 1)
	s.Assert().Equal("global", order[0])
}

func (s *SourceHooksSuite) TestOnFailureCalledAfterGlobal() {
	var order []string

	source := &sourceWithHooks{name: "test"}

	r := New(WithOnFailure(func(ctx context.Context, src, key string, err error, d time.Duration) {
		order = append(order, "global")
	}))
	r.AddSource(source)
	Register(r, "test", &testHandler{err: errors.New("fail")})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Error(err)
	s.Assert().True(source.onFailureCalled)
	s.Require().Len(order, 1)
	s.Assert().Equal("global", order[0])
}

func (s *SourceHooksSuite) TestSourceOnNoHandlerCanOverrideGlobalSkip() {
	source := &sourceWithHooks{
		name:           "test",
		onNoHandlerErr: errors.New("source says fail"),
	}

	r := New(WithOnNoHandler(func(ctx context.Context, src, key string) error {
		return nil
	}))
	r.AddSource(source)

	msg := []byte(`{"type": "unknown", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Assert().True(source.onNoHandlerCalled)
	s.Assert().Error(err)
}

func (s *SourceHooksSuite) TestGlobalOnNoHandlerErrorPreventsSourceHook() {
	globalErr := errors.New("global error")
	source := &sourceWithHooks{
		name:           "test",
		onNoHandlerErr: errors.New("source error"),
	}

	r := New(WithOnNoHandler(func(ctx context.Context, src, key string) error {
		return globalErr
	}))
	r.AddSource(source)

	msg := []byte(`{"type": "unknown", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.Assert().ErrorIs(err, globalErr)
}

func (s *SourceHooksSuite) TestSourceOnUnmarshalErrorCanOverrideGlobal() {
	source := &sourceWithHooks{
		name:                "test",
		onUnmarshalErrorErr: errors.New("source says fail"),
	}

	r := New(WithOnUnmarshalError(func(ctx context.Context, src, key string, err error) error {
		return nil
	}))
	r.AddSource(source)
	Register(r, "test", &testHandler{})

	msg := []byte(`{"type": "test", "payload": "invalid"}`)
	err := r.Process(context.Background(), msg)

	s.Assert().True(source.onUnmarshalErrorCalled)
	s.Assert().Error(err)
}

type SourceHooksContextPropagationSuite struct {
	suite.Suite
}

func TestSourceHooksContextPropagationSuite(t *testing.T) {
	suite.Run(t, new(SourceHooksContextPropagationSuite))
}

func (s *SourceHooksContextPropagationSuite) TestSourceOnParseContextAvailableToHandler() {
	source := &sourceWithHooks{name: "test"}

	var handlerCtx context.Context
	r := New()
	r.AddSource(source)

	RegisterFunc(r, "test", func(ctx context.Context, p testPayload) error {
		handlerCtx = ctx
		return nil
	})

	msg := []byte(`{"type": "test", "payload": {}}`)
	err := r.Process(context.Background(), msg)

	s.NoError(err)
	s.Assert().Equal("called", handlerCtx.Value(contextKey("source-hook")))
}
