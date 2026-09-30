package importsync

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// Mode describes who is authoritative after an import. Standalone makes
// OwnGit the primary home; coexistence keeps the external host authoritative.
// Both use the same divergence-safe publication rule; the mode is recorded and
// reported so bindings and owners can tell the intended relationship.
type Mode string

const (
	ModeStandalone  Mode = "standalone"
	ModeCoexistence Mode = "coexistence"
)

func (m Mode) valid() bool {
	return m == ModeStandalone || m == ModeCoexistence
}

// canonicalSourceURL applies the rules the confined transport enforces, so
// configuration fails before any network work. The canonical string is the
// persisted source identity; a change advances the generation.
func canonicalSourceURL(raw string, maxBytes int, allowPlainHTTP bool) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 8192
	}
	parsed, err := importfetch.ParseSourceURL(raw, maxBytes, allowPlainHTTP)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// deriveRepositoryName turns a source URL into a portable repository name. Name
// collisions reuse the existing repository; an explicit name overrides this.
func deriveRepositoryName(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "import"
	}
	base := strings.TrimSuffix(path.Base(parsed.Path), ".git")
	var builder strings.Builder
	for _, character := range base {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '.', character == '_', character == '-':
			builder.WriteRune(character)
		default:
			builder.WriteRune('-')
		}
	}
	name := strings.Trim(builder.String(), "-._")
	if len(name) > 100 {
		name = strings.TrimRight(name[:100], "-._")
	}
	if name == "" || !isASCIIAlphaNumeric(name[0]) || repository.ValidateID(strings.ToLower(name)) != nil {
		return "import"
	}
	return name
}

func isASCIIAlphaNumeric(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9')
}

// maxSelectedRefNameBytes bounds a published branch or tag name, and the
// source HEAD target, so every name recorded for the import fits
// state.MaxImportRefNameBytes. The longest recorded name derived from a
// published ref is its provenance retention name,
// refs/owngit/provenance/<kind>/<short>/<oid>: for both refs/heads/ and
// refs/tags/ it is 19 bytes plus the object ID longer than the ref itself.
// The bound uses the SHA-256 width so it does not depend on the source's
// object format: 500 - 19 - 64 = 417 bytes. The advertisement parser accepts
// longer names, which stay importable in namespaces that are not published.
const maxSelectedRefNameBytes = state.MaxImportRefNameBytes - (len("refs/owngit/provenance/heads//") - len("refs/heads/")) - 64

func refNameTooLong(name string) string {
	return fmt.Sprintf("ref %q... is %d bytes; imported branch, tag, and HEAD target names are limited to %d bytes", name[:64], len(name), maxSelectedRefNameBytes)
}

// importedRef reports whether a refresh publishes the named ref: a branch,
// a tag, or a ref in one of the source's extra namespaces. The extra
// namespaces never overlap refs/owngit/ (state.ValidateExtraRefPrefixes).
func importedRef(name string, extra []string) bool {
	if strings.HasPrefix(name, "refs/heads/") || strings.HasPrefix(name, "refs/tags/") {
		return true
	}
	for _, prefix := range extra {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// selectedRefs is the published subset of one advertisement: branches, tags
// and the source's extra namespaces. Other namespaces are reported, never
// published, and a case-only collision is refused because a case-insensitive
// filesystem cannot hold both names. A name longer than
// maxSelectedRefNameBytes is refused before any pack is indexed.
type selectedRefs struct {
	refs     []importgit.Ref
	upstream map[string]string
	skipped  []string
}

func selectRefs(advertisement *importgit.Advertisement, extra []string) (selectedRefs, error) {
	result := selectedRefs{upstream: map[string]string{}}
	folded := map[string]string{}
	for _, ref := range advertisement.Refs {
		name := ref.Name
		switch {
		case name == "HEAD":
			// HEAD is tracked separately through the advertisement's HEAD facts.
			continue
		case !importedRef(name, extra):
			result.skipped = append(result.skipped, name)
			continue
		}
		if len(name) > maxSelectedRefNameBytes {
			return selectedRefs{}, fmt.Errorf("source %s", refNameTooLong(name))
		}
		if previous, exists := folded[strings.ToLower(name)]; exists && previous != name {
			return selectedRefs{}, fmt.Errorf("refs %q and %q differ only by case; the destination representation is unsupported", previous, name)
		}
		folded[strings.ToLower(name)] = name
		result.refs = append(result.refs, ref)
		result.upstream[name] = ref.OID
	}
	sort.Slice(result.refs, func(left, right int) bool { return result.refs[left].Name < result.refs[right].Name })
	sort.Strings(result.skipped)
	return result, nil
}

// advertisedRefs returns every advertised named ref except HEAD. These refs are
// mirrored only in unpublished staging so validation does not depend on the
// smaller branch and tag publication selection.
func advertisedRefs(advertisement *importgit.Advertisement) []importgit.Ref {
	refs := make([]importgit.Ref, 0, len(advertisement.Refs))
	for _, ref := range advertisement.Refs {
		if ref.Name != "HEAD" {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left].Name < refs[right].Name })
	return refs
}

// wantedOIDs is every object named by an advertised ref, HEAD fact, or peel
// fact, including objects in namespaces that OwnGit will not publish.
func wantedOIDs(advertisement *importgit.Advertisement) []string {
	seen := map[string]bool{}
	var wanted []string
	add := func(oid string) {
		if oid == "" || seen[oid] {
			return
		}
		seen[oid] = true
		wanted = append(wanted, oid)
	}
	for _, ref := range advertisement.Refs {
		add(ref.OID)
		add(ref.PeeledOID)
	}
	if advertisement.Head.Advertised {
		add(advertisement.Head.OID)
		add(advertisement.Head.PeeledOID)
	}
	sort.Strings(wanted)
	return wanted
}

// advertisedTipOIDs returns the advertised roots whose reachable objects are
// included in content inspection. Peel facts are derived from these roots.
func advertisedTipOIDs(advertisement *importgit.Advertisement) []string {
	seen := map[string]bool{}
	var roots []string
	add := func(oid string) {
		if oid == "" || seen[oid] {
			return
		}
		seen[oid] = true
		roots = append(roots, oid)
	}
	for _, ref := range advertisement.Refs {
		add(ref.OID)
	}
	if advertisement.Head.Advertised {
		add(advertisement.Head.OID)
	}
	sort.Strings(roots)
	return roots
}

// headSymrefTarget returns the supported symbolic HEAD target. Advertisement
// adoption rejects every nonempty target outside refs/heads before staging.
func headSymrefTarget(advertisement *importgit.Advertisement) string {
	return advertisement.Head.SymrefTarget
}

// refKind maps a branch or tag to the retention naming component used by
// the Git update hook. Refs in extra namespaces have no kind: their replaced
// tips are not kept.
func refKind(name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		return "heads", true
	case strings.HasPrefix(name, "refs/tags/"):
		return "tags", true
	}
	return "", false
}

func refShortName(name string) string {
	if index := strings.Index(name[len("refs/"):], "/"); index >= 0 {
		return name[len("refs/")+index+1:]
	}
	return name
}
