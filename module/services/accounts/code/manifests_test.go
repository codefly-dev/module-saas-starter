package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cacheAgentFloor is the oldest published redis agent release the cache
// dependency may be pinned to. accounts declares with-read-replicas: true on
// it (module/services/cache/service.codefly.yaml), which runs several redis
// processes that share one unsynchronized log writer below this floor;
// codefly-dev/service-redis d00ed105 serializes that writer, and v0.0.99 is
// the first published release that carries it (v0.0.97, the prior pin, does
// not — merge-base against the two tags confirms the split). Below the
// floor, the composition panics under load with a slice bound like [84:0]
// as one process's line is appended while another's is cut out of the
// shared buffer.
var cacheAgentFloor = [3]int{0, 0, 99}

// The cache dependency must be pinned at or above the floor, because nothing
// else serializes the log writer every redis process in a replicated
// composition shares.
//
// This is a gate rather than an integration test on purpose: the panic is a
// load-dependent race in the dependency's own runtime, not something the
// contract path can observe by resolving successfully, and a version
// comparison is decidable offline, needs no server, and cannot be defeated
// by what happens to be running.
func TestTheCacheAgentIsPinnedAboveTheReplicaLogWriterFloor(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "cache", "service.codefly.yaml"))
	if err != nil {
		t.Fatalf("read the cache service manifest: %v", err)
	}
	var svc struct {
		Agent struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &svc); err != nil {
		t.Fatalf("parse the cache service manifest: %v", err)
	}
	if svc.Agent.Name != "redis" {
		t.Fatalf("the cache provider's agent is %q; this gate knows only redis's release history", svc.Agent.Name)
	}

	got, err := semverTriple(svc.Agent.Version)
	if err != nil {
		t.Fatalf("agent.version %q: %v", svc.Agent.Version, err)
	}
	if got.Less(cacheAgentFloor) {
		t.Fatalf("the cache agent is pinned to %s, below the floor %d.%d.%d: below it, a composition that runs "+
			"read replicas panics under load on the shared, unsynchronized log writer. See cacheAgentFloor",
			svc.Agent.Version, cacheAgentFloor[0], cacheAgentFloor[1], cacheAgentFloor[2])
	}
}

// triple is a major.minor.patch version, compared field by field.
type triple [3]int

func (v triple) Less(other [3]int) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func semverTriple(s string) (triple, error) {
	var v triple
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("want major.minor.patch, got %d part(s)", len(parts))
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("part %d (%q) is not a number", i+1, p)
		}
		v[i] = n
	}
	return v, nil
}
