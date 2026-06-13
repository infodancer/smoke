package smoke_test

import (
	"fmt"

	"github.com/infodancer/smoke"
)

// Register routes through the registry, then ask which lack smoke coverage --
// the check behind `smolder gate`.
func Example_gate() {
	reg := smoke.New()
	reg.AddPattern("GET /health")                                  // no params -> covered
	reg.AddPattern("GET /widgets/{id}", smoke.Example("id", "42")) // example -> covered
	reg.AddPattern("GET /orders/{id}")                             // missing example -> not covered
	reg.AddPattern("POST /widgets", smoke.Write())                 // write -> skipped, covered

	for _, r := range reg.Manifest().Incomplete() {
		fmt.Printf("needs coverage: %s %s\n", r.EffectiveMethod(), r.Pattern)
	}
	// Output: needs coverage: GET /orders/{id}
}

// Example params are substituted into a route's pattern to build the concrete
// path the runner probes.
func ExampleManifestRoute_ExpandPath() {
	reg := smoke.New()
	reg.AddPattern("GET /campaign/{slug}/sessions/{id}/{$}",
		smoke.Example("slug", "shadowmaze"), smoke.Example("id", "3"))

	path, _ := reg.Manifest().Routes[0].ExpandPath()
	fmt.Println(path)
	// Output: /campaign/shadowmaze/sessions/3/
}
