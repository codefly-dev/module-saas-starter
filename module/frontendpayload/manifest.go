package frontendpayload

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/solutionhost"
)

type federationAssets struct {
	JS, CSS struct{ Sync, Async []string }
}

// checkManifest admits the maintained producer's automatic public path profile.
// The manifest inventory establishes declared closure, not arbitrary JS safety.
func (b *Bundle) checkManifest(frontend *solutionhost.Frontend) error {
	var manifest struct {
		MetaData struct {
			RemoteEntry   struct{ Name, Path, Type string }
			PublicPath    string
			GetPublicPath json.RawMessage
			GlobalName    string
		}
		Exposes []struct {
			Path   string
			Assets federationAssets
		}
		Shared  []struct{ Assets federationAssets }
		Remotes []json.RawMessage
	}
	if err := json.Unmarshal(b.files["mf-manifest.json"], &manifest); err != nil {
		return errors.New("frontend bundle has no readable federation manifest")
	}
	meta := manifest.MetaData
	if meta.PublicPath != "auto" || len(meta.GetPublicPath) != 0 || len(manifest.Remotes) != 0 {
		return errors.New("frontend bundle requires automatic public path without executable overrides or remotes")
	}
	if meta.RemoteEntry.Type != "global" || meta.GlobalName == "" {
		return errors.New("frontend bundle requires a named global remote entry")
	}
	entry := meta.RemoteEntry.Name
	if meta.RemoteEntry.Path != "" {
		entry = strings.TrimSuffix(meta.RemoteEntry.Path, "/") + "/" + entry
	}
	if err := b.requireFile(entry); err != nil {
		return err
	}
	found := false
	for _, expose := range manifest.Exposes {
		found = found || expose.Path == frontend.ExposedModule
		if err := b.checkAssets(expose.Assets); err != nil {
			return err
		}
	}
	if !found {
		return errors.New("frontend bundle does not expose the declared module")
	}
	for _, shared := range manifest.Shared {
		if err := b.checkAssets(shared.Assets); err != nil {
			return err
		}
	}
	for _, surface := range frontend.Surfaces {
		if !strings.HasPrefix(surface.Module, "/assets/") {
			return errors.New("frontend surface must name a file beneath /assets/")
		}
		if err := b.requireFile(strings.TrimPrefix(surface.Module, "/assets/")); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bundle) checkAssets(assets federationAssets) error {
	for _, group := range [][]string{assets.JS.Sync, assets.JS.Async, assets.CSS.Sync, assets.CSS.Async} {
		for _, name := range group {
			if err := b.requireFile(name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Bundle) requireFile(name string) error {
	// A filesystem filename is not necessarily a relative URL: forbid URL
	// selectors too, before the loader can interpret them differently.
	if strings.ContainsAny(name, ":?#") {
		return errors.New("frontend asset must be a relative file path")
	}
	if _, _, err := b.Open(name); err != nil {
		return fmt.Errorf("frontend bundle is missing a canonical asset: %s", name)
	}
	return nil
}
