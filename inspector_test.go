package dispatch

import (
	"testing"
)

func TestJSONInspector_Inspect(t *testing.T) {
	inspector := JSONInspector()

	t.Run("returns view for valid JSON", func(t *testing.T) {
		raw := []byte(`{"foo": "bar"}`)
		view, err := inspector.Inspect(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if view == nil {
			t.Fatal("expected view, got nil")
		}
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		raw := []byte(`{not valid}`)
		_, err := inspector.Inspect(raw)
		if err != ErrInvalidJSON {
			t.Fatalf("expected ErrInvalidJSON, got %v", err)
		}
	})

	t.Run("returns error for empty input", func(t *testing.T) {
		_, err := inspector.Inspect([]byte{})
		if err != ErrInvalidJSON {
			t.Fatalf("expected ErrInvalidJSON, got %v", err)
		}
	})
}

func TestJSONView_HasField(t *testing.T) {
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

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		path   string
		exists bool
	}{
		{"source", true},
		{"detail-type", true},
		{"detail", true},
		{"detail.userId", true},
		{"detail.nested.deep", true},
		{"missing", false},
		{"detail.missing", false},
		{"detail.nested.missing", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := view.HasField(tt.path)
			if got != tt.exists {
				t.Errorf("HasField(%q) = %v, want %v", tt.path, got, tt.exists)
			}
		})
	}
}

func TestJSONView_GetString(t *testing.T) {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"count": 42,
		"active": true,
		"detail": {
			"userId": "123"
		}
	}`)

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("returns string value", func(t *testing.T) {
		val, ok := view.GetString("source")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if val != "my.app" {
			t.Errorf("got %q, want %q", val, "my.app")
		}
	})

	t.Run("returns nested string value", func(t *testing.T) {
		val, ok := view.GetString("detail.userId")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if val != "123" {
			t.Errorf("got %q, want %q", val, "123")
		}
	})

	t.Run("returns false for number", func(t *testing.T) {
		_, ok := view.GetString("count")
		if ok {
			t.Error("expected ok=false for number field")
		}
	})

	t.Run("returns false for boolean", func(t *testing.T) {
		_, ok := view.GetString("active")
		if ok {
			t.Error("expected ok=false for boolean field")
		}
	})

	t.Run("returns false for missing field", func(t *testing.T) {
		_, ok := view.GetString("missing")
		if ok {
			t.Error("expected ok=false for missing field")
		}
	})
}

func TestJSONView_GetBytes(t *testing.T) {
	inspector := JSONInspector()
	raw := []byte(`{
		"source": "my.app",
		"count": 42,
		"detail": {"userId": "123"}
	}`)

	view, err := inspector.Inspect(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("returns raw string with quotes", func(t *testing.T) {
		val, ok := view.GetBytes("source")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if string(val) != `"my.app"` {
			t.Errorf("got %s, want %q", val, `"my.app"`)
		}
	})

	t.Run("returns raw number", func(t *testing.T) {
		val, ok := view.GetBytes("count")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if string(val) != "42" {
			t.Errorf("got %s, want %q", val, "42")
		}
	})

	t.Run("returns raw object", func(t *testing.T) {
		val, ok := view.GetBytes("detail")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if string(val) != `{"userId": "123"}` {
			t.Errorf("got %s, want %q", val, `{"userId": "123"}`)
		}
	})

	t.Run("returns false for missing field", func(t *testing.T) {
		_, ok := view.GetBytes("missing")
		if ok {
			t.Error("expected ok=false for missing field")
		}
	})
}
