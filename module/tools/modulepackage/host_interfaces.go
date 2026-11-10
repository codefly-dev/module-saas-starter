package modulepackage

// The host-interface declaration gate. module.codefly.yaml declares what a
// service implements (`implements: codefly.dev/<interface>@<version>`), and the
// service's go.mod pins the codefly-dev/authority revision whose conformance
// suite its tests run. The two are one fact only if each declared version is
// the version the interface publishes at that revision, or a patch below it
// within the same major.minor: a declaration above it names a version no suite
// proved, and a declaration in another major.minor names a suite that did not
// run (passing 0.5.0's suite says nothing about 0.4.0's). A patch below is
// reported and never failed, because moving the pin to run a newer patch's
// suite must not force the declaration before that suite is green.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/codefly-dev/core/resources"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"gopkg.in/yaml.v3"
)

// AuthorityLibraryPrefix is the module path every published authority library
// shares: the conformance suite, the core that decides, the client, delivery
// and resources.
const AuthorityLibraryPrefix = "github.com/codefly-dev/authority/libraries/"

// ConformanceLibrary is the shared suite itself; a service that pins no suite
// has nothing proving what it declares.
const ConformanceLibrary = AuthorityLibraryPrefix + "conformance/go"

// authorityPublisher is the publisher of every interface codefly-dev/authority
// carries under interfaces/<name>/interface.codefly.yaml.
const authorityPublisher = "codefly.dev"

// HostInterfaceDeclaration is one `implements` entry of a service's interface
// endpoint: <publisher>/<name>@<version>.
type HostInterfaceDeclaration struct {
	Service   string
	Publisher string
	Name      string
	Version   string
}

// ID is the declaration as a consumer names it, publisher/name.
func (d HostInterfaceDeclaration) ID() string { return d.Publisher + "/" + d.Name }

// AuthorityPin is the one codefly-dev/authority revision a go.mod pins its
// authority libraries at, read from their pseudo-versions and the commits
// their tags were cut from.
type AuthorityPin struct {
	// Revision is the commit the pins name, abbreviated as a pseudo-version
	// abbreviates it.
	Revision string
	// Libraries are the pinned authority modules, by path, sorted.
	Libraries []string
}

// PublishedInterface is interfaces/<name>/interface.codefly.yaml as authority
// publishes it at a revision.
type PublishedInterface struct {
	Publisher string `yaml:"publisher"`
	Name      string `yaml:"name"`
	Version   string `yaml:"version"`
}

// HostInterfaceFinding is the gate's answer for one declaration.
type HostInterfaceFinding struct {
	Interface string
	Declared  string
	Published string
	// Failure says the declaration is not proven; otherwise Reason, when set,
	// is a notice.
	Failure bool
	Reason  string
}

// HostInterfaceOptions name what the gate reads.
type HostInterfaceOptions struct {
	// ModuleRoot holds module.codefly.yaml and services/<service>/.
	ModuleRoot string
	// InterfacesDir is authority's interfaces/ tree at the pinned revision.
	InterfacesDir string
	// Revision, when given, is the commit InterfacesDir was read at; it must be
	// the commit the go.mod pins name.
	Revision string
}

// DeclaredHostInterfaces reads every `implements` entry of the module's
// interface endpoints, through core's own module loader.
func DeclaredHostInterfaces(moduleRoot string) ([]HostInterfaceDeclaration, error) {
	mod, err := resources.LoadModuleFromDir(context.Background(), moduleRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(moduleRoot, resources.ModuleConfigurationName), err)
	}
	var declared []HostInterfaceDeclaration
	seen := map[string]bool{}
	if mod.Interface == nil {
		return nil, nil
	}
	for _, endpoint := range mod.Interface.Endpoints {
		for _, entry := range endpoint.Implements {
			d, err := parseDeclaration(endpoint.Service, entry)
			if err != nil {
				return nil, err
			}
			key := d.Service + " " + d.ID() + "@" + d.Version
			if seen[key] {
				continue
			}
			seen[key] = true
			declared = append(declared, d)
		}
	}
	sort.Slice(declared, func(i, j int) bool {
		if declared[i].Service != declared[j].Service {
			return declared[i].Service < declared[j].Service
		}
		return declared[i].ID() < declared[j].ID()
	})
	return declared, nil
}

func parseDeclaration(service, entry string) (HostInterfaceDeclaration, error) {
	at := strings.LastIndex(entry, "@")
	slash := strings.Index(entry, "/")
	if at < 0 || slash < 0 || slash > at || slash == 0 || at == len(entry)-1 {
		return HostInterfaceDeclaration{}, fmt.Errorf("service %s implements %q, which is not <publisher>/<name>@<version>", service, entry)
	}
	return HostInterfaceDeclaration{Service: service, Publisher: entry[:slash], Name: entry[slash+1 : at], Version: entry[at+1:]}, nil
}

// ResolveTag answers the full commit a tagged module version was cut from,
// for the go.mod in dir.
type ResolveTag func(dir, modulePath, version string) (string, error)

// ResolveTagOrigin resolves a tag through the go command: `go mod download
// -json` reports the origin commit of the version it serves, from the module
// cache when the version is there and from the module proxy otherwise.
func ResolveTagOrigin(dir, modulePath, version string) (string, error) {
	cmd := exec.Command("go", "mod", "download", "-json", modulePath+"@"+version)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("go mod download %s@%s: %w: %s", modulePath, version, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("go mod download %s@%s: %w", modulePath, version, err)
	}
	var info struct {
		Origin *struct{ Hash string }
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("go mod download %s@%s: %w", modulePath, version, err)
	}
	if info.Origin == nil || info.Origin.Hash == "" {
		return "", fmt.Errorf("go mod download %s@%s records no origin commit", modulePath, version)
	}
	return info.Origin.Hash, nil
}

// ReadAuthorityPin reads the authority revision a go.mod pins: every direct
// requirement under AuthorityLibraryPrefix names a commit, by its
// pseudo-version or by the commit resolve answers for its tag, and all of
// them must name one commit, because the suite, the core it decides with and
// the client are run together. A nil resolve refuses a tag. A replace or an exclude of an
// authority library is refused: the require line would then name nothing the
// suite runs (authority's own services replace these libraries with local
// directories, which is right there and never here).
func ReadAuthorityPin(goModPath string, resolve ResolveTag) (AuthorityPin, error) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return AuthorityPin{}, err
	}
	file, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return AuthorityPin{}, err
	}
	for _, replace := range file.Replace {
		if strings.HasPrefix(replace.Old.Path, AuthorityLibraryPrefix) {
			return AuthorityPin{}, fmt.Errorf("%s replaces %s with %s, so its require line names nothing the suite runs; a replaced authority library is refused", goModPath, replace.Old.Path, strings.TrimSpace(replace.New.Path+" "+replace.New.Version))
		}
	}
	for _, exclude := range file.Exclude {
		if strings.HasPrefix(exclude.Mod.Path, AuthorityLibraryPrefix) {
			return AuthorityPin{}, fmt.Errorf("%s excludes %s %s, which moves the authority libraries away from the revision the require lines name; an excluded authority library is refused", goModPath, exclude.Mod.Path, exclude.Mod.Version)
		}
	}
	var pin AuthorityPin
	revisions := map[string][]string{}
	for _, req := range file.Require {
		if req.Indirect || !strings.HasPrefix(req.Mod.Path, AuthorityLibraryPrefix) {
			continue
		}
		rev, err := pinnedRevision(goModPath, req.Mod, resolve)
		if err != nil {
			return AuthorityPin{}, err
		}
		revisions[rev] = append(revisions[rev], shortLibraries([]string{req.Mod.Path})...)
		pin.Libraries = append(pin.Libraries, req.Mod.Path)
	}
	if len(pin.Libraries) == 0 {
		return AuthorityPin{}, fmt.Errorf("%s pins no %s* library, so no conformance suite proves what the service declares", goModPath, AuthorityLibraryPrefix)
	}
	if len(revisions) > 1 {
		var split []string
		for rev, libraries := range revisions {
			sort.Strings(libraries)
			split = append(split, rev+" ("+strings.Join(libraries, ", ")+")")
		}
		sort.Strings(split)
		return AuthorityPin{}, fmt.Errorf("%s pins the authority libraries at two revisions, %s: the suite, the core it decides with and the client are one revision or the pass proves nothing", goModPath, strings.Join(split, " and "))
	}
	sort.Strings(pin.Libraries)
	if !slices.Contains(pin.Libraries, ConformanceLibrary) {
		return AuthorityPin{}, fmt.Errorf("%s pins no %s, so no shared suite runs against what the service declares", goModPath, ConformanceLibrary)
	}
	for rev := range revisions {
		pin.Revision = rev
	}
	return pin, nil
}

// pinnedRevision is the commit one requirement names, abbreviated to the
// length a pseudo-version carries so a tag and a pseudo-version of one commit
// compare equal.
func pinnedRevision(goModPath string, mod module.Version, resolve ResolveTag) (string, error) {
	if module.IsPseudoVersion(mod.Version) {
		rev, err := module.PseudoVersionRev(mod.Version)
		if err != nil {
			return "", fmt.Errorf("%s pins %s at %s: %w", goModPath, mod.Path, mod.Version, err)
		}
		return rev, nil
	}
	if resolve == nil {
		return "", fmt.Errorf("%s pins %s at %s, which names no commit without resolving its tag", goModPath, mod.Path, mod.Version)
	}
	hash, err := resolve(filepath.Dir(goModPath), mod.Path, mod.Version)
	if err != nil {
		return "", fmt.Errorf("%s pins %s at %s, whose commit cannot be resolved: %w", goModPath, mod.Path, mod.Version, err)
	}
	const abbreviated = 12
	if len(hash) < abbreviated {
		return "", fmt.Errorf("%s pins %s at %s, resolved to %q, which is not a commit", goModPath, mod.Path, mod.Version, hash)
	}
	return hash[:abbreviated], nil
}

// ReadPublishedInterfaces reads <dir>/<name>/interface.codefly.yaml for every
// interface authority publishes at the revision dir was read from.
func ReadPublishedInterfaces(dir string) (map[string]PublishedInterface, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	published := map[string]PublishedInterface{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), "interface.codefly.yaml")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var iface PublishedInterface
		if err := yaml.Unmarshal(data, &iface); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if iface.Name != entry.Name() {
			return nil, fmt.Errorf("%s names interface %q, not %q", path, iface.Name, entry.Name())
		}
		published[iface.Publisher+"/"+iface.Name] = iface
	}
	if len(published) == 0 {
		return nil, fmt.Errorf("%s publishes no interface (no <name>/interface.codefly.yaml)", dir)
	}
	return published, nil
}

var documentedInterface = regexp.MustCompile(`codefly\.dev/([a-z][a-z0-9-]*)@([0-9]+\.[0-9]+\.[0-9]+)`)

// DocumentedHostInterfaces reads the versions a service's HOST_INTERFACE.md
// names in its opening section, before its first heading: the sentence that
// tells a reader what the service implements, held here to the manifest so it
// cannot drift from it. A service with no such document documents nothing.
func DocumentedHostInterfaces(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	opening, _, _ := strings.Cut(string(data), "\n## ")
	documented := map[string][]string{}
	for _, match := range documentedInterface.FindAllStringSubmatch(opening, -1) {
		id := authorityPublisher + "/" + match[1]
		if !slices.Contains(documented[id], match[2]) {
			documented[id] = append(documented[id], match[2])
		}
	}
	return documented, nil
}

// CompareHostInterfaces judges each declaration against what the pinned
// revision publishes and what the service's document says. A failure is a
// declared version above the published one, a declared version in another
// major.minor than the published one (no suite of that minor ran), an
// interface the revision does not publish, a version that is not semver, or a
// document naming another version; a declared patch below the published one,
// within the same major.minor, is a notice.
func CompareHostInterfaces(declared []HostInterfaceDeclaration, published map[string]PublishedInterface, documented map[string][]string) []HostInterfaceFinding {
	var findings []HostInterfaceFinding
	for _, d := range declared {
		finding := HostInterfaceFinding{Interface: d.ID(), Declared: d.Version}
		if d.Publisher != authorityPublisher {
			finding.Reason = "not published by codefly-dev/authority; no shared suite here proves it"
			findings = append(findings, finding)
			continue
		}
		p, ok := published[d.ID()]
		if !ok {
			finding.Failure = true
			finding.Reason = "the pinned authority revision publishes no such interface, so no suite proves the declaration"
			findings = append(findings, finding)
			continue
		}
		finding.Published = p.Version
		declaredVersion, err := semver.StrictNewVersion(d.Version)
		if err != nil {
			finding.Failure = true
			finding.Reason = fmt.Sprintf("the declared version is not semver: %v", err)
			findings = append(findings, finding)
			continue
		}
		publishedVersion, err := semver.StrictNewVersion(p.Version)
		if err != nil {
			finding.Failure = true
			finding.Reason = fmt.Sprintf("the published version is not semver: %v", err)
			findings = append(findings, finding)
			continue
		}
		switch declaredVersion.Compare(publishedVersion) {
		case 1:
			finding.Failure = true
			finding.Reason = "the declaration claims a version the pinned suite does not prove; move the pin to a revision publishing it and pass that suite first"
		case -1:
			if declaredVersion.Major() != publishedVersion.Major() || declaredVersion.Minor() != publishedVersion.Minor() {
				finding.Failure = true
				finding.Reason = fmt.Sprintf("the pinned suite is %s's and no %d.%d suite ran, so the declaration is unproven; pin a revision publishing %d.%d, or declare %s once its suite is green",
					p.Version, declaredVersion.Major(), declaredVersion.Minor(), declaredVersion.Major(), declaredVersion.Minor(), p.Version)
			} else {
				finding.Reason = fmt.Sprintf("the pinned suite proves %s, a patch above the declaration; declaring it is a manifest change backed by this pin", p.Version)
			}
		}
		if versions, ok := documented[d.ID()]; ok {
			if len(versions) != 1 || versions[0] != d.Version {
				finding.Failure = true
				finding.Reason = fmt.Sprintf("HOST_INTERFACE.md names %s where the manifest declares %s", strings.Join(versions, " and "), d.Version)
			}
		} else if documented != nil {
			finding.Failure = true
			finding.Reason = "HOST_INTERFACE.md does not name this interface in its opening section"
		}
		findings = append(findings, finding)
	}
	return findings
}

// VerifyHostInterfaces runs the gate over the module: every service declaring
// a host interface, its go.mod pin, the interfaces published at that pin and
// its HOST_INTERFACE.md. It prints one line per declaration to out and returns
// an error naming every failure.
func VerifyHostInterfaces(options HostInterfaceOptions, out io.Writer) error {
	if options.InterfacesDir == "" {
		return errors.New("verify-host-interfaces needs --interfaces, authority's interfaces/ tree at the pinned revision")
	}
	declared, err := DeclaredHostInterfaces(options.ModuleRoot)
	if err != nil {
		return err
	}
	if len(declared) == 0 {
		return fmt.Errorf("%s declares no host interface; the gate measures nothing", filepath.Join(options.ModuleRoot, resources.ModuleConfigurationName))
	}
	published, err := ReadPublishedInterfaces(options.InterfacesDir)
	if err != nil {
		return err
	}
	byService := map[string][]HostInterfaceDeclaration{}
	var services []string
	for _, d := range declared {
		if _, ok := byService[d.Service]; !ok {
			services = append(services, d.Service)
		}
		byService[d.Service] = append(byService[d.Service], d)
	}
	sort.Strings(services)
	var failures []string
	for _, service := range services {
		goMod := filepath.Join(options.ModuleRoot, "services", service, "code", "go.mod")
		pin, err := ReadAuthorityPin(goMod, ResolveTagOrigin)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("service %s declares host interfaces but has no Go module at %s, so no pinned suite proves them", service, goMod)
			}
			return err
		}
		if options.Revision != "" && !strings.HasPrefix(options.Revision, pin.Revision) {
			return fmt.Errorf("the interfaces were read at %s, but %s pins %s", options.Revision, goMod, pin.Revision)
		}
		documented, err := DocumentedHostInterfaces(filepath.Join(options.ModuleRoot, "services", service, "HOST_INTERFACE.md"))
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%s: host interfaces declared by %s, held to authority %s (%s)\n", service, goMod, pin.Revision, strings.Join(shortLibraries(pin.Libraries), ", "))
		for _, finding := range CompareHostInterfaces(byService[service], published, documented) {
			verdict := "ok"
			switch {
			case finding.Failure:
				verdict = "FAIL: " + finding.Reason
				failures = append(failures, fmt.Sprintf("%s %s declared %s, published %s: %s", service, finding.Interface, finding.Declared, orNone(finding.Published), finding.Reason))
			case finding.Reason != "":
				verdict = "notice: " + finding.Reason
			}
			_, _ = fmt.Fprintf(out, "  %-28s declared %-8s published %-8s %s\n", finding.Interface, finding.Declared, orNone(finding.Published), verdict)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d host interface declaration(s) are not proven by the pinned suite:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	return nil
}

func shortLibraries(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(path, AuthorityLibraryPrefix), "/go"))
	}
	return out
}

func orNone(version string) string {
	if version == "" {
		return "none"
	}
	return version
}
