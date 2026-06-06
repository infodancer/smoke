package smoke

import "sync"

// Registry records the routes a host serves and their smoke specs. It is the
// single source of truth from which the manifest is generated; integration
// adapters (the stdlib Mux wrap, or future harvest adapters) feed it.
type Registry struct {
	mu     sync.Mutex
	specs  []RouteSpec
	prefix string
}

// New returns an empty registry.
func New() *Registry { return &Registry{} }

// Add records a route. method/path are the parsed registration pattern;
// options layer on example params, expected status, effect, or skip. The
// effect defaults from the method unless an option overrides it.
func (r *Registry) Add(method, path string, opts ...Option) {
	spec := RouteSpec{
		Method:  method,
		Pattern: r.prefix + path,
		Effect:  defaultEffect(method),
	}
	for _, o := range opts {
		o(&spec)
	}
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
}

// AddPattern records a route from a full Go 1.22 registration pattern
// ("GET /x" or "/x"), parsing the method out.
func (r *Registry) AddPattern(pattern string, opts ...Option) {
	method, path := parsePattern(pattern)
	r.Add(method, path, opts...)
}

// Subtree records a coarse spec for a mounted handler that owns a path prefix
// (e.g. a third-party module not yet converted to register its own specs).
// The prefix is probed as-is.
func (r *Registry) Subtree(prefix string, opts ...Option) {
	r.Add("GET", prefix, opts...)
}

// Specs returns a copy of the recorded specs.
func (r *Registry) Specs() []RouteSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RouteSpec(nil), r.specs...)
}

// Manifest builds the transportable manifest from the recorded specs.
func (r *Registry) Manifest() Manifest {
	specs := r.Specs()
	routes := make([]ManifestRoute, len(specs))
	for i, s := range specs {
		routes[i] = toManifestRoute(s)
	}
	return Manifest{Routes: routes}
}
