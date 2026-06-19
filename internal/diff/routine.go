package diff

import (
	"reflect"
	"strconv"

	"sigs.k8s.io/yaml"
)

// onlyImageOrVersionChanges reports whether every changed resource differs from
// its prior render ONLY in container image references and/or chart-version
// metadata — the "helm.sh/chart" / "app.kubernetes.io/version" labels, a Flux
// source's version ref (OCIRepository/HelmRepository/GitRepository ref or a
// HelmRelease chart version), and image refs pinned in values. It is the
// structural half of the "routine" signal (see api.DiffResult.Routine).
//
// It compares the manifests in their marshaled (rendered) form — the same YAML
// the diff shows — so a field that differs only in Go type/representation (e.g.
// an int vs a float that marshal to identical text) is NOT counted: the reviewer
// never sees it in the diff, so it must not flip a routine bump to non-routine.
//
// The bias is deliberately conservative: an added or removed resource, or any
// single *visible* changed field outside the allowlist, makes the whole PR not
// routine. A false "routine" must never hide a structural change.
func onlyImageOrVersionChanges(changes []Change) bool {
	if len(changes) == 0 {
		return false // nothing real changed — not a "routine bump" worth a pill
	}
	for _, c := range changes {
		// A whole resource appearing or disappearing is structural, never routine.
		if c.Status == statusAdded || c.Status == statusRemoved {
			return false
		}
		// Diff the normalized (marshal→reparse) manifests, not the raw maps: the
		// raw values can differ only by Go type yet render identically, which would
		// otherwise count as a phantom change the reviewer can't see.
		paths := changedPaths(normalizeForDiff(c.Old), normalizeForDiff(c.New))
		if len(paths) == 0 {
			return false // marshaled-equal no-ops are dropped upstream; be safe
		}
		for _, p := range paths {
			if !routineField(c.Kind, p) {
				return false
			}
		}
	}
	return true
}

// normalizeForDiff round-trips a manifest through the same marshal the diff view
// uses (sigs.k8s.io/yaml → JSON number/type rules), so both sides of a comparison
// share one representation and only differences that actually render survive.
// Returns the input unchanged if it can't be marshaled (changedPaths then falls
// back to the raw values — no worse than before).
func normalizeForDiff(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return m
	}
	var out map[string]any
	if err := yaml.Unmarshal(b, &out); err != nil {
		return m
	}
	return out
}

// changedPaths walks two manifests in parallel and returns the field paths whose
// leaf values differ (added, removed, or changed). Map keys and array indices
// become path segments; an array whose length changed is reported at the array's
// own path — a structural edit (a container/volume added or removed), not a leaf
// tweak, so the allowlist rejects it.
func changedPaths(oldV, newV any) [][]string {
	var out [][]string
	var walk func(o, n any, path []string)
	walk = func(o, n any, path []string) {
		om, omOK := o.(map[string]any)
		nm, nmOK := n.(map[string]any)
		if omOK && nmOK {
			seen := make(map[string]struct{}, len(om)+len(nm))
			for k := range om {
				seen[k] = struct{}{}
			}
			for k := range nm {
				seen[k] = struct{}{}
			}
			for k := range seen {
				walk(om[k], nm[k], appendSeg(path, k))
			}
			return
		}
		os, osOK := o.([]any)
		ns, nsOK := n.([]any)
		if osOK && nsOK {
			if len(os) != len(ns) {
				out = append(out, path)
				return
			}
			for i := range os {
				walk(os[i], ns[i], appendSeg(path, strconv.Itoa(i)))
			}
			return
		}
		if !reflect.DeepEqual(o, n) {
			out = append(out, path)
		}
	}
	walk(oldV, newV, nil)
	return out
}

// appendSeg returns prefix+seg as a fresh slice so sibling recursions never
// clobber each other's stored paths through a shared backing array.
func appendSeg(prefix []string, seg string) []string {
	np := make([]string, len(prefix)+1)
	copy(np, prefix)
	np[len(prefix)] = seg
	return np
}

// routineField reports whether one changed path is an image/version field
// konflate treats as routine. Anything else — env, args, command, resources,
// replicas, RBAC rules, ports, volumes, … — returns false.
func routineField(kind string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	// Any container's image ref: containers/initContainers/ephemeralContainers
	// all expose it as an "image" leaf, as does a CR's single spec.image.
	if path[len(path)-1] == "image" {
		return true
	}
	// An image ref split across values — "<…>.image.tag" / "<…>.image.digest"
	// (app-template / common-chart style, where a bump lands in a HelmRelease's
	// spec.values rather than the rendered container's single image leaf).
	if n := len(path); n >= 2 && path[n-2] == "image" && (path[n-1] == "tag" || path[n-1] == "digest") {
		return true
	}
	// Chart-version metadata Helm/Flux stamp onto every rendered resource.
	if pathEq(path, "metadata", "labels", "helm.sh/chart") ||
		pathEq(path, "metadata", "labels", "app.kubernetes.io/version") {
		return true
	}
	// A Flux source/release advancing its pinned version.
	switch kind {
	case "OCIRepository", "HelmRepository":
		return pathEq(path, "spec", "ref", "tag") ||
			pathEq(path, "spec", "ref", "digest") ||
			pathEq(path, "spec", "ref", "semver")
	case "GitRepository":
		return pathEq(path, "spec", "ref", "tag")
	case "HelmRelease":
		return pathEq(path, "spec", "chart", "spec", "version")
	}
	return false
}

func pathEq(path []string, segs ...string) bool {
	if len(path) != len(segs) {
		return false
	}
	for i := range segs {
		if path[i] != segs[i] {
			return false
		}
	}
	return true
}
