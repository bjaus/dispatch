package dispatch

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type DiscriminatorSuite struct {
	suite.Suite
	inspector Inspector
	view      View
}

func (s *DiscriminatorSuite) SetupTest() {
	s.inspector = JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated",
		"detail": {"userId": "123"}
	}`)

	var err error
	s.view, err = s.inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestDiscriminatorSuite(t *testing.T) {
	suite.Run(t, new(DiscriminatorSuite))
}

func (s *DiscriminatorSuite) TestHasFields_MatchesWhenAllFieldsPresent() {
	d := HasFields("source", "detail-type")
	s.Assert().True(d.Match(s.view))
}

func (s *DiscriminatorSuite) TestHasFields_MatchesNestedFields() {
	d := HasFields("source", "detail.userId")
	s.Assert().True(d.Match(s.view))
}

func (s *DiscriminatorSuite) TestHasFields_FailsWhenAnyFieldMissing() {
	d := HasFields("source", "missing")
	s.Assert().False(d.Match(s.view))
}

func (s *DiscriminatorSuite) TestHasFields_MatchesWithNoFields() {
	d := HasFields()
	s.Assert().True(d.Match(s.view))
}

type FieldEqualsSuite struct {
	suite.Suite
	inspector Inspector
	view      View
}

func (s *FieldEqualsSuite) SetupTest() {
	s.inspector = JSONInspector()
	raw := []byte(`{
		"Type": "Notification",
		"source": "my.app",
		"count": 42
	}`)

	var err error
	s.view, err = s.inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestFieldEqualsSuite(t *testing.T) {
	suite.Run(t, new(FieldEqualsSuite))
}

func (s *FieldEqualsSuite) TestMatchesExactStringValue() {
	d := FieldEquals("Type", "Notification")
	s.Assert().True(d.Match(s.view))
}

func (s *FieldEqualsSuite) TestFailsOnWrongValue() {
	d := FieldEquals("Type", "Other")
	s.Assert().False(d.Match(s.view))
}

func (s *FieldEqualsSuite) TestFailsOnMissingField() {
	d := FieldEquals("missing", "value")
	s.Assert().False(d.Match(s.view))
}

func (s *FieldEqualsSuite) TestFailsOnNonStringField() {
	d := FieldEquals("count", "42")
	s.Assert().False(d.Match(s.view))
}

type AndSuite struct {
	suite.Suite
	inspector Inspector
	view      View
}

func (s *AndSuite) SetupTest() {
	s.inspector = JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated"
	}`)

	var err error
	s.view, err = s.inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestAndSuite(t *testing.T) {
	suite.Run(t, new(AndSuite))
}

func (s *AndSuite) TestMatchesWhenAllMatch() {
	d := And(
		HasFields("source"),
		FieldEquals("detail-type", "UserCreated"),
	)
	s.Assert().True(d.Match(s.view))
}

func (s *AndSuite) TestFailsWhenAnyFails() {
	d := And(
		HasFields("source"),
		FieldEquals("detail-type", "Other"),
	)
	s.Assert().False(d.Match(s.view))
}

func (s *AndSuite) TestMatchesWithNoDiscriminators() {
	d := And()
	s.Assert().True(d.Match(s.view))
}

type OrSuite struct {
	suite.Suite
	inspector Inspector
	view      View
}

func (s *OrSuite) SetupTest() {
	s.inspector = JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated"
	}`)

	var err error
	s.view, err = s.inspector.Inspect(raw)
	s.Require().NoError(err)
}

func TestOrSuite(t *testing.T) {
	suite.Run(t, new(OrSuite))
}

func (s *OrSuite) TestMatchesWhenAnyMatches() {
	d := Or(
		FieldEquals("detail-type", "Other"),
		FieldEquals("detail-type", "UserCreated"),
	)
	s.Assert().True(d.Match(s.view))
}

func (s *OrSuite) TestFailsWhenNoneMatch() {
	d := Or(
		FieldEquals("detail-type", "Other"),
		HasFields("missing"),
	)
	s.Assert().False(d.Match(s.view))
}

func (s *OrSuite) TestFailsWithNoDiscriminators() {
	d := Or()
	s.Assert().False(d.Match(s.view))
}

type ComposedDiscriminatorsSuite struct {
	suite.Suite
	inspector Inspector
}

func (s *ComposedDiscriminatorsSuite) SetupTest() {
	s.inspector = JSONInspector()
}

func TestComposedDiscriminatorsSuite(t *testing.T) {
	suite.Run(t, new(ComposedDiscriminatorsSuite))
}

func (s *ComposedDiscriminatorsSuite) TestEventBridgeStyleDiscriminator() {
	eventBridge := HasFields("source", "detail-type", "detail")

	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated",
		"detail": {"userId": "123"}
	}`)
	view, err := s.inspector.Inspect(raw)
	s.Require().NoError(err)

	s.Assert().True(eventBridge.Match(view))
}

func (s *ComposedDiscriminatorsSuite) TestSNSStyleDiscriminator() {
	sns := FieldEquals("Type", "Notification")

	raw := []byte(`{
		"Type": "Notification",
		"Message": "{}"
	}`)
	view, err := s.inspector.Inspect(raw)
	s.Require().NoError(err)

	s.Assert().True(sns.Match(view))
}

func (s *ComposedDiscriminatorsSuite) TestComplexComposedDiscriminator() {
	d := Or(
		HasFields("source", "detail-type", "detail"),
		FieldEquals("Type", "Notification"),
	)

	eventBridgeRaw := []byte(`{"source": "x", "detail-type": "y", "detail": {}}`)
	snsRaw := []byte(`{"Type": "Notification", "Message": "{}"}`)
	otherRaw := []byte(`{"foo": "bar"}`)

	ebView, err := s.inspector.Inspect(eventBridgeRaw)
	s.Require().NoError(err)
	snsView, err := s.inspector.Inspect(snsRaw)
	s.Require().NoError(err)
	otherView, err := s.inspector.Inspect(otherRaw)
	s.Require().NoError(err)

	s.Assert().True(d.Match(ebView))
	s.Assert().True(d.Match(snsView))
	s.Assert().False(d.Match(otherView))
}
