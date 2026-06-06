package smoke

import "net/http"

// Mux is the stdlib-ServeMux integration: a drop-in wrapper that records a
// spec for every registration and delegates to a real *http.ServeMux. It is
// needed because net/http exposes no way to enumerate a ServeMux's routes, so
// the route set can only be captured at registration time.
//
// HandleFunc and Handle keep the stdlib signatures plus a variadic of smoke
// Options, so existing registration calls compile unchanged and pick up specs
// incrementally — a call with no options is recorded as an (incomplete unless
// parameterless) route, which is exactly what the gate flags.
type Mux struct {
	mux *http.ServeMux
	reg *Registry
}

// NewMux returns a Mux backed by a fresh ServeMux and Registry.
func NewMux() *Mux {
	return &Mux{mux: http.NewServeMux(), reg: New()}
}

// WrapMux returns a Mux that records into reg and delegates to m.
func WrapMux(m *http.ServeMux, reg *Registry) *Mux {
	return &Mux{mux: m, reg: reg}
}

// HandleFunc registers a handler function and records its spec.
func (m *Mux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request), opts ...Option) {
	m.reg.AddPattern(pattern, opts...)
	m.mux.HandleFunc(pattern, handler)
}

// Handle registers a handler and records its spec.
func (m *Mux) Handle(pattern string, handler http.Handler, opts ...Option) {
	m.reg.AddPattern(pattern, opts...)
	m.mux.Handle(pattern, handler)
}

// ServeHTTP delegates to the underlying ServeMux, so *Mux is itself an
// http.Handler and middleware can wrap it directly.
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

// ServeMux returns the underlying *http.ServeMux, for mounting third-party
// handlers that require the concrete type. Routes registered directly on it
// are not recorded — declare a Registry.Subtree spec for those.
func (m *Mux) ServeMux() *http.ServeMux { return m.mux }

// Registry returns the spec registry, for manifest generation and the gate.
func (m *Mux) Registry() *Registry { return m.reg }
