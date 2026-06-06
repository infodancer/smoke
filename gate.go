package smoke

// Incomplete returns the routes that lack smoke coverage — a registered route
// with unfilled path params and no Skip. These are the gate's targets: in warn
// mode they are printed as a prompt; in fail mode their presence fails CI.
func (m Manifest) Incomplete() []ManifestRoute {
	var out []ManifestRoute
	for _, r := range m.Routes {
		if !r.Complete {
			out = append(out, r)
		}
	}
	return out
}
