package objectstore

// defaultMaxObjects caps how many objects one sync takes when the source does
// not configure a limit; maxObjectsCeiling is the hard upper bound regardless of
// configuration.
const (
	defaultMaxObjects = 100
	maxObjectsCeiling = 1000
)

// Scope is which of a prefix's objects one sync takes: those whose key InScope
// admits — the host's key filter for the source, as a GitHub source's
// SourceConfig.InScope is its path scope — up to the object cap. It belongs to
// the connector, not to what lists the prefix: a listing source hands Window
// everything it listed, so replacing the listing source leaves the filter and
// the cap where they are.
type Scope struct {
	InScope    func(key string) bool // nil admits every key
	MaxObjects int                   // 0 means the default cap
}

func (s Scope) limit() int {
	switch {
	case s.MaxObjects <= 0:
		return defaultMaxObjects
	case s.MaxObjects > maxObjectsCeiling:
		return maxObjectsCeiling
	}
	return s.MaxObjects
}

// Window keeps the listed items whose key the scope admits, in listing order
// and at most the cap of them. An item it refuses is dropped before it is
// counted, so an excluded object never uses up the cap and is never fetched.
// whole is false when the cap cut an admitted item: a caller that reads
// absence as deletion must then not act on what it kept.
func Window(s Scope, items []Item) (kept []Item, whole bool) {
	limit := s.limit()
	kept = make([]Item, 0, min(len(items), limit))
	admitted := 0
	for _, item := range items {
		if s.InScope != nil && !s.InScope(item.Key) {
			continue
		}
		if admitted++; admitted <= limit {
			kept = append(kept, item)
		}
	}
	return kept, admitted <= limit
}
