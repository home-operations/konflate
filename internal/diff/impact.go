package diff

import (
	"maps"
	"slices"

	"github.com/home-operations/konflate/internal/api"
)

// Summarize computes the blast-radius banner (api.Impact) over the changed
// resources: total count, distinct producing parents, the sorted set of
// affected namespaces, and how many CustomResourceDefinitions changed.
func Summarize(changes []Change) api.Impact {
	imp := api.Impact{Resources: len(changes)}

	parents := make(map[string]struct{})
	namespaces := make(map[string]struct{})
	for _, c := range changes {
		if c.Parent != "" {
			parents[c.Parent] = struct{}{}
		}
		if c.Namespace != "" {
			namespaces[c.Namespace] = struct{}{}
		}
		if c.Kind == "CustomResourceDefinition" {
			imp.CRDs++
		}
	}

	imp.Parents = len(parents)
	if len(namespaces) > 0 {
		imp.Namespaces = slices.Sorted(maps.Keys(namespaces))
	}
	return imp
}
