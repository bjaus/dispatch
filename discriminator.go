package dispatch

// Discriminator determines if a source should handle a message based on
// the message content. Discriminators are cheap to evaluate compared to
// full parsing.
type Discriminator interface {
	Match(v View) bool
}

// HasFields returns a Discriminator that matches when all paths exist.
func HasFields(paths ...string) Discriminator {
	return hasFields{paths: paths}
}

type hasFields struct {
	paths []string
}

func (d hasFields) Match(v View) bool {
	for _, p := range d.paths {
		if !v.HasField(p) {
			return false
		}
	}
	return true
}

// FieldEquals returns a Discriminator that matches when the path exists
// and equals the given string value.
func FieldEquals(path, value string) Discriminator {
	return fieldEquals{path: path, value: value}
}

type fieldEquals struct {
	path  string
	value string
}

func (d fieldEquals) Match(v View) bool {
	s, ok := v.GetString(d.path)
	return ok && s == d.value
}

// And returns a Discriminator that matches when all discriminators match.
func And(ds ...Discriminator) Discriminator {
	return and{ds: ds}
}

type and struct {
	ds []Discriminator
}

func (d and) Match(v View) bool {
	for _, disc := range d.ds {
		if !disc.Match(v) {
			return false
		}
	}
	return true
}

// Or returns a Discriminator that matches when any discriminator matches.
func Or(ds ...Discriminator) Discriminator {
	return or{ds: ds}
}

type or struct {
	ds []Discriminator
}

func (d or) Match(v View) bool {
	for _, disc := range d.ds {
		if disc.Match(v) {
			return true
		}
	}
	return false
}
