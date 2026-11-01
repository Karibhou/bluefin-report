package ui

import "testing"

// An empty collection must not be reported as a search miss, even with a
// query retained across a refresh.
func TestEmptyStatePrefersEmptyCollection(t *testing.T) {
	for _, c := range []struct {
		total uint32
		query string
		want  string
	}{
		{0, "", "No Metrics Available"},
		{0, "cpu", "No Metrics Available"},
		{5, "cpu", "No Results Found"},
	} {
		if got, _ := emptyState(c.total, c.query); got != c.want {
			t.Errorf("emptyState(%d, %q) = %q, want %q", c.total, c.query, got, c.want)
		}
	}
}
