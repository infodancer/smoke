package smoke

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Manifest is the serialized route set -- the contract between the smoke
// library (producer) and the smolder CLI (consumer). It is the union of every
// registered route and its completeness state, so the gate can flag
// spec-less routes offline from the JSON alone.
type Manifest struct {
	Routes []ManifestRoute `json:"routes"`
}

// ManifestRoute is a RouteSpec plus its computed Complete flag, flattened for
// transport. Effect serializes as a readable string.
type ManifestRoute struct {
	Method        string            `json:"method"`
	Pattern       string            `json:"pattern"`
	Effect        string            `json:"effect"`
	ExampleParams map[string]string `json:"example_params,omitempty"`
	ExpectStatus  int               `json:"expect_status,omitempty"`
	Skip          string            `json:"skip,omitempty"`
	RequestBody   string            `json:"request_body,omitempty"`
	ContentType   string            `json:"content_type,omitempty"`
	AuthRequired  bool              `json:"auth_required,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Complete      bool              `json:"complete"`
}

func effectFromString(s string) Effect {
	if s == "mutating" {
		return Mutating
	}
	return ReadOnly
}

// toManifestRoute flattens a spec for transport, computing Complete.
func toManifestRoute(s RouteSpec) ManifestRoute {
	return ManifestRoute{
		Method:        s.Method,
		Pattern:       s.Pattern,
		Effect:        s.Effect.String(),
		ExampleParams: s.ExampleParams,
		ExpectStatus:  s.ExpectStatus,
		Skip:          s.Skip,
		RequestBody:   s.RequestBody,
		ContentType:   s.ContentType,
		AuthRequired:  s.AuthRequired,
		Labels:        s.Labels,
		Complete:      s.Complete(),
	}
}

// Spec rebuilds the RouteSpec from a transported route (loses the recomputed
// Complete, which the consumer re-derives or reads from the flag).
func (r ManifestRoute) Spec() RouteSpec {
	return RouteSpec{
		Method:        r.Method,
		Pattern:       r.Pattern,
		Effect:        effectFromString(r.Effect),
		ExampleParams: r.ExampleParams,
		ExpectStatus:  r.ExpectStatus,
		Skip:          r.Skip,
		RequestBody:   r.RequestBody,
		ContentType:   r.ContentType,
		AuthRequired:  r.AuthRequired,
		Labels:        r.Labels,
	}
}

// EffectiveMethod is the method to probe with -- GET when none was registered.
func (r ManifestRoute) EffectiveMethod() string {
	if r.Method == "" {
		return "GET"
	}
	return r.Method
}

// MarshalJSON emits routes in a stable order so a committed manifest diffs
// cleanly and the drift check is deterministic.
func (m Manifest) MarshalJSON() ([]byte, error) {
	routes := append([]ManifestRoute(nil), m.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		return routes[i].Method < routes[j].Method
	})
	type alias struct {
		Routes []ManifestRoute `json:"routes"`
	}
	return json.MarshalIndent(alias{Routes: routes}, "", "  ")
}

// ParseManifest decodes a manifest from JSON.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("smoke: parse manifest: %w", err)
	}
	return m, nil
}

// ExpandPath substitutes a route's example params into its pattern, producing a
// concrete request path. It drops the {$} anchor, replaces {name} and
// {name...} wildcards with their example values, and reports an error if any
// wildcard lacks a value.
func (r ManifestRoute) ExpandPath() (string, error) {
	_, path := parsePattern(r.Pattern)
	if path == "" {
		path = r.Pattern
	}
	// Drop the end-of-path anchor.
	path = strings.ReplaceAll(path, "{$}", "")
	var missing []string
	out := paramPattern.ReplaceAllStringFunc(path, func(tok string) string {
		name := paramPattern.FindStringSubmatch(tok)[1]
		v, ok := r.ExampleParams[name]
		if !ok {
			missing = append(missing, name)
			return tok
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("smoke: %s %s missing example params: %s",
			r.EffectiveMethod(), r.Pattern, strings.Join(missing, ", "))
	}
	return out, nil
}
