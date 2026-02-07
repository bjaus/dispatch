package dispatch

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type JSONInspectorSuite struct {
	suite.Suite
	inspector Inspector
}

func (s *JSONInspectorSuite) SetupTest() {
	s.inspector = JSONInspector()
}

func TestJSONInspectorSuite(t *testing.T) {
	suite.Run(t, new(JSONInspectorSuite))
}

func (s *JSONInspectorSuite) TestReturnsViewForValidJSON() {
	raw := []byte(`{"foo": "bar"}`)
	view, err := s.inspector.Inspect(raw)

	s.Require().NoError(err)
	s.Assert().NotNil(view)
}

func (s *JSONInspectorSuite) TestReturnsErrorForInvalidJSON() {
	raw := []byte(`{not valid}`)
	_, err := s.inspector.Inspect(raw)

	s.Assert().ErrorIs(err, ErrInvalidJSON)
}

func (s *JSONInspectorSuite) TestReturnsErrorForEmptyInput() {
	_, err := s.inspector.Inspect([]byte{})

	s.Assert().ErrorIs(err, ErrInvalidJSON)
}

type JSONViewHasFieldSuite struct {
	suite.Suite
	view View
}

func (s *JSONViewHasFieldSuite) SetupTest() {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated",
		"detail": {
			"userId": "123",
			"nested": {
				"deep": true
			}
		}
	}`)

	var err error
	s.view, err = inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestJSONViewHasFieldSuite(t *testing.T) {
	suite.Run(t, new(JSONViewHasFieldSuite))
}

func (s *JSONViewHasFieldSuite) TestHasField() {
	tests := map[string]struct {
		path   string
		exists bool
	}{
		"source":                {"source", true},
		"detail-type":           {"detail-type", true},
		"detail":                {"detail", true},
		"detail.userId":         {"detail.userId", true},
		"detail.nested.deep":    {"detail.nested.deep", true},
		"missing":               {"missing", false},
		"detail.missing":        {"detail.missing", false},
		"detail.nested.missing": {"detail.nested.missing", false},
	}

	for name, tt := range tests {
		s.Run(name, func() {
			got := s.view.HasField(tt.path)
			s.Assert().Equal(tt.exists, got)
		})
	}
}

type JSONViewGetStringSuite struct {
	suite.Suite
	view View
}

func (s *JSONViewGetStringSuite) SetupTest() {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"count": 42,
		"active": true,
		"detail": {
			"userId": "123"
		}
	}`)

	var err error
	s.view, err = inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestJSONViewGetStringSuite(t *testing.T) {
	suite.Run(t, new(JSONViewGetStringSuite))
}

func (s *JSONViewGetStringSuite) TestReturnsStringValue() {
	val, ok := s.view.GetString("source")

	s.Require().True(ok)
	s.Assert().Equal("my.app", val)
}

func (s *JSONViewGetStringSuite) TestReturnsNestedStringValue() {
	val, ok := s.view.GetString("detail.userId")

	s.Require().True(ok)
	s.Assert().Equal("123", val)
}

func (s *JSONViewGetStringSuite) TestReturnsFalseForNumber() {
	_, ok := s.view.GetString("count")

	s.Assert().False(ok)
}

func (s *JSONViewGetStringSuite) TestReturnsFalseForBoolean() {
	_, ok := s.view.GetString("active")

	s.Assert().False(ok)
}

func (s *JSONViewGetStringSuite) TestReturnsFalseForMissingField() {
	_, ok := s.view.GetString("missing")

	s.Assert().False(ok)
}

type JSONViewGetBytesSuite struct {
	suite.Suite
	view View
}

func (s *JSONViewGetBytesSuite) SetupTest() {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"count": 42,
		"detail": {"userId": "123"}
	}`)

	var err error
	s.view, err = inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestJSONViewGetBytesSuite(t *testing.T) {
	suite.Run(t, new(JSONViewGetBytesSuite))
}

func (s *JSONViewGetBytesSuite) TestReturnsRawStringWithQuotes() {
	val, ok := s.view.GetBytes("source")

	s.Require().True(ok)
	s.Assert().Equal(`"my.app"`, string(val))
}

func (s *JSONViewGetBytesSuite) TestReturnsRawNumber() {
	val, ok := s.view.GetBytes("count")

	s.Require().True(ok)
	s.Assert().Equal("42", string(val))
}

func (s *JSONViewGetBytesSuite) TestReturnsRawObject() {
	val, ok := s.view.GetBytes("detail")

	s.Require().True(ok)
	s.Assert().Equal(`{"userId": "123"}`, string(val))
}

func (s *JSONViewGetBytesSuite) TestReturnsFalseForMissingField() {
	_, ok := s.view.GetBytes("missing")

	s.Assert().False(ok)
}
