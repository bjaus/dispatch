package dispatch

import (
	"errors"

	"github.com/tidwall/gjson"
)

// ErrInvalidJSON is returned when the input is not valid JSON.
var ErrInvalidJSON = errors.New("invalid JSON")

// Inspector examines raw bytes and returns a View for field queries.
// Different inspectors handle different formats (JSON, protobuf, etc.).
type Inspector interface {
	Inspect(raw []byte) (View, error)
}

// View provides format-agnostic field access for discriminator matching.
type View interface {
	// HasField returns true if the path exists in the message.
	HasField(path string) bool

	// GetString returns the string value at path, or false if not found
	// or not a string.
	GetString(path string) (string, bool)

	// GetBytes returns the raw bytes at path, or false if not found.
	// For JSON, this returns the raw JSON value (including quotes for strings).
	GetBytes(path string) ([]byte, bool)
}

// JSONInspector returns an Inspector that uses gjson for field access.
func JSONInspector() Inspector {
	return jsonInspector{}
}

type jsonInspector struct{}

func (jsonInspector) Inspect(raw []byte) (View, error) {
	if !gjson.ValidBytes(raw) {
		return nil, ErrInvalidJSON
	}
	return jsonView{raw: raw}, nil
}

type jsonView struct {
	raw []byte
}

func (v jsonView) HasField(path string) bool {
	return gjson.GetBytes(v.raw, path).Exists()
}

func (v jsonView) GetString(path string) (string, bool) {
	r := gjson.GetBytes(v.raw, path)
	if !r.Exists() {
		return "", false
	}
	if r.Type != gjson.String {
		return "", false
	}
	return r.String(), true
}

func (v jsonView) GetBytes(path string) ([]byte, bool) {
	r := gjson.GetBytes(v.raw, path)
	if !r.Exists() {
		return nil, false
	}
	return []byte(r.Raw), true
}
