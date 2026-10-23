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
// dependency may be pinned to. It is the highest of three independent
// requirements, each breaking something below its own threshold:
//
//   - v0.0.95 first emits the `cache` configuration group `cache.Open` reads
//     (service-redis #76). Below it, accounts/code's pkg/infra/redis.go
//     STOPGAP (#927) is the only way to reach this dependency at all.
//   - v0.0.99 serializes the log writer every redis process shares
//     (service-redis d00ed105). Below it, a composition that runs
//     replicas — accounts declares with-read-replicas: true on it
//     (module/services/cache/service.codefly.yaml) — panics under load
//     with a slice bound like [84:0] as one process's line is appended
//     while another's is cut out of the shared buffer.
//   - v0.0.100 answers the BuildCapabilities RPC so a selected Buildx
//     builder is honoured (service-redis e75e956, #117). Below it, a
//     render that mirrors a registry fails on this agent with "method
//     BuildCapabilities not implemented".
//
// Each threshold is confirmed by merge-base against the service-redis tags,
// not trusted from a release note. So the floor is v0.0.100.
var cacheAgentFloor = [3]int{0, 0, 100}

// The cache dependency must be pinned at or above the floor: nothing else
// emits the `cache` group, serializes the log writer every redis process in
// a replicated composition shares, or answers BuildCapabilities.
//
// This is a gate rather than an integration test on purpose: none of the
// three failures is something the contract path can observe by resolving
// successfully — a panic is a load-dependent race, and a missing RPC only
// surfaces under a registry-mirrored render. A version comparison is
// decidable offline, needs no server, and cannot be defeated by what happens
// to be running.
func TestTheCacheAgentIsPinnedAboveTheFloor(t *testing.T) {
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
		t.Fatalf("the cache agent is pinned to %s, below the floor %d.%d.%d: below it, the cache group may not "+
			"resolve, a composition that runs read replicas panics under load on the shared log writer, or a "+
			"registry-mirrored render fails on BuildCapabilities. See cacheAgentFloor",
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
