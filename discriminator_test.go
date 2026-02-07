package dispatch

import (
	"testing"
)

func TestHasFields(t *testing.T) {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated",
		"detail": {"userId": "123"}
	}`)

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("matches when all fields present", func(t *testing.T) {
		d := HasFields("source", "detail-type")
		if !d.Match(view) {
			t.Error("expected match")
		}
	})

	t.Run("matches nested fields", func(t *testing.T) {
		d := HasFields("source", "detail.userId")
		if !d.Match(view) {
			t.Error("expected match")
		}
	})

	t.Run("fails when any field missing", func(t *testing.T) {
		d := HasFields("source", "missing")
		if d.Match(view) {
			t.Error("expected no match")
		}
	})

	t.Run("matches with no fields (vacuous truth)", func(t *testing.T) {
		d := HasFields()
		if !d.Match(view) {
			t.Error("expected match for empty field list")
		}
	})
}

func TestFieldEquals(t *testing.T) {
	inspector := JSONInspector()
	raw := []byte(`{
		"Type": "Notification",
		"source": "my.app",
		"count": 42
	}`)

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("matches exact string value", func(t *testing.T) {
		d := FieldEquals("Type", "Notification")
		if !d.Match(view) {
			t.Error("expected match")
		}
	})

	t.Run("fails on wrong value", func(t *testing.T) {
		d := FieldEquals("Type", "Other")
		if d.Match(view) {
			t.Error("expected no match")
		}
	})

	t.Run("fails on missing field", func(t *testing.T) {
		d := FieldEquals("missing", "value")
		if d.Match(view) {
			t.Error("expected no match")
		}
	})

	t.Run("fails on non-string field", func(t *testing.T) {
		d := FieldEquals("count", "42")
		if d.Match(view) {
			t.Error("expected no match for non-string field")
		}
	})
}

func TestAnd(t *testing.T) {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated"
	}`)

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("matches when all match", func(t *testing.T) {
		d := And(
			HasFields("source"),
			FieldEquals("detail-type", "UserCreated"),
		)
		if !d.Match(view) {
			t.Error("expected match")
		}
	})

	t.Run("fails when any fails", func(t *testing.T) {
		d := And(
			HasFields("source"),
			FieldEquals("detail-type", "Other"),
		)
		if d.Match(view) {
			t.Error("expected no match")
		}
	})

	t.Run("matches with no discriminators (vacuous truth)", func(t *testing.T) {
		d := And()
		if !d.Match(view) {
			t.Error("expected match for empty And")
		}
	})
}

func TestOr(t *testing.T) {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"detail-type": "UserCreated"
	}`)

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("matches when any matches", func(t *testing.T) {
		d := Or(
			FieldEquals("detail-type", "Other"),
			FieldEquals("detail-type", "UserCreated"),
		)
		if !d.Match(view) {
			t.Error("expected match")
		}
	})

	t.Run("fails when none match", func(t *testing.T) {
		d := Or(
			FieldEquals("detail-type", "Other"),
			HasFields("missing"),
		)
		if d.Match(view) {
			t.Error("expected no match")
		}
	})

	t.Run("fails with no discriminators", func(t *testing.T) {
		d := Or()
		if d.Match(view) {
			t.Error("expected no match for empty Or")
		}
	})
}

func TestComposedDiscriminators(t *testing.T) {
	inspector := JSONInspector()

	t.Run("EventBridge-style discriminator", func(t *testing.T) {
		eventBridge := HasFields("source", "detail-type", "detail")

		raw := []byte(`{
			"source": "my.app",
			"detail-type": "UserCreated",
			"detail": {"userId": "123"}
		}`)
		view, _ := inspector.Inspect(raw)

		if !eventBridge.Match(view) {
			t.Error("expected EventBridge match")
		}
	})

	t.Run("SNS-style discriminator", func(t *testing.T) {
		sns := FieldEquals("Type", "Notification")

		raw := []byte(`{
			"Type": "Notification",
			"Message": "{}"
		}`)
		view, _ := inspector.Inspect(raw)

		if !sns.Match(view) {
			t.Error("expected SNS match")
		}
	})

	t.Run("complex composed discriminator", func(t *testing.T) {
		// Match EventBridge OR SNS
		d := Or(
			HasFields("source", "detail-type", "detail"),
			FieldEquals("Type", "Notification"),
		)

		eventBridgeRaw := []byte(`{"source": "x", "detail-type": "y", "detail": {}}`)
		snsRaw := []byte(`{"Type": "Notification", "Message": "{}"}`)
		otherRaw := []byte(`{"foo": "bar"}`)

		ebView, _ := inspector.Inspect(eventBridgeRaw)
		snsView, _ := inspector.Inspect(snsRaw)
		otherView, _ := inspector.Inspect(otherRaw)

		if !d.Match(ebView) {
			t.Error("expected EventBridge match")
		}
		if !d.Match(snsView) {
			t.Error("expected SNS match")
		}
		if d.Match(otherView) {
			t.Error("expected no match for other")
		}
	})
}
