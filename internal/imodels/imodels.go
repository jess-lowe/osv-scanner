package imodels

import (
	"fmt"
	"log"
	"strings"

	"github.com/google/osv-scalibr/extractor"
	"github.com/google/osv-scalibr/extractor/filesystem/language/golang/gobinary"
	"github.com/google/osv-scalibr/extractor/filesystem/language/java/archive"
	"github.com/google/osv-scalibr/extractor/filesystem/language/python/wheelegg"
	"github.com/google/osv-scalibr/extractor/filesystem/os/apk"
	"github.com/google/osv-scalibr/extractor/filesystem/os/dpkg"
	"github.com/google/osv-scalibr/extractor/filesystem/os/rpm"
	"github.com/google/osv-scalibr/extractor/filesystem/sbom/cdx"
	"github.com/google/osv-scalibr/extractor/filesystem/sbom/spdx"
	"github.com/google/osv-scanner/v2/internal/cachedregexp"
	"github.com/google/osv-scanner/v2/internal/imodels/ecosystem"
	"github.com/google/osv-scanner/v2/internal/scalibrextract/language/javascript/nodemodules"
	"github.com/google/osv-scanner/v2/internal/scalibrextract/vcs/gitrepo"
	"github.com/google/osv-scanner/v2/internal/utility/purl"
	"github.com/google/osv-scanner/v2/internal/utility/semverlike"

	"github.com/google/osv-scanner/v2/pkg/models"
	"github.com/ossf/osv-schema/bindings/go/osvschema"

	scalibrosv "github.com/google/osv-scalibr/extractor/filesystem/osv"
)

var sbomExtractors = map[string]struct{}{
	spdx.Extractor{}.Name(): {},
	cdx.Extractor{}.Name():  {},
}

var gitExtractors = map[string]struct{}{
	gitrepo.Extractor{}.Name(): {},
}

var osExtractors = map[string]struct{}{
	dpkg.Extractor{}.Name(): {},
	apk.Extractor{}.Name():  {},
	rpm.Extractor{}.Name():  {},
}

var artifactExtractors = map[string]struct{}{
	nodemodules.Extractor{}.Name(): {},
	gobinary.Extractor{}.Name():    {},
	archive.Extractor{}.Name():     {},
	wheelegg.Extractor{}.Name():    {},
}

// PackageInfo provides getter functions for commonly used fields of inventory
// and applies transformations when required for use in osv-scanner
type PackageInfo struct {
	// purlCache is used to cache the special case for SBOMs where we convert Name, Version, and Ecosystem from purls
	// extracted from the SBOM
	purlCache *models.PackageInfo
	*extractor.Inventory
}

func (pkg *PackageInfo) Name() string {
	// TODO(v2): SBOM special case, to be removed after PURL to ESI conversion within each extractor is complete
	if pkg.purlCache != nil {
		return pkg.purlCache.Name
	}

	// --- Make specific patches to names as necessary ---
	// Patch Go package to stdlib
	if pkg.Ecosystem().Ecosystem == osvschema.EcosystemGo && pkg.Inventory.Name == "go" {
		return "stdlib"
	}

	// TODO: Move the normalization to another where matching logic happens.
	// Patch python package names to be normalized
	if pkg.Ecosystem().Ecosystem == osvschema.EcosystemPyPI {
		// per https://peps.python.org/pep-0503/#normalized-names
		return strings.ToLower(cachedregexp.MustCompile(`[-_.]+`).ReplaceAllLiteralString(pkg.Inventory.Name, "-"))
	}

	// Patch Maven archive extractor package names
	if metadata, ok := pkg.Inventory.Metadata.(*archive.Metadata); ok {
		// Debian uses source name on osv.dev
		// (fallback to using the normal name if source name is empty)
		if metadata.ArtifactID != "" && metadata.GroupID != "" {
			return metadata.GroupID + ":" + metadata.ArtifactID
		}
	}

	// --- OS metadata ---
	if metadata, ok := pkg.Inventory.Metadata.(*dpkg.Metadata); ok {
		// Debian uses source name on osv.dev
		// (fallback to using the normal name if source name is empty)
		if metadata.SourceName != "" {
			return metadata.SourceName
		}
	}

	if metadata, ok := pkg.Inventory.Metadata.(*apk.Metadata); ok {
		if metadata.OriginName != "" {
			return metadata.OriginName
		}
	}

	return pkg.Inventory.Name
}

func (pkg *PackageInfo) Ecosystem() ecosystem.Parsed {
	ecosystemStr := pkg.Inventory.Ecosystem()

	// TODO(v2): SBOM special case, to be removed after PURL to ESI conversion within each extractor is complete
	if pkg.purlCache != nil {
		ecosystemStr = pkg.purlCache.Ecosystem
	}

	// TODO: Maybe cache this parse result
	eco, err := ecosystem.Parse(ecosystemStr)
	if err != nil {
		// Ignore this error for now as we can't do too much about an unknown ecosystem
		// TODO(v2): Replace with slog
		log.Printf("Warning: %s\n", err.Error())
	}

	return eco
}

func (pkg *PackageInfo) Version() string {
	// TODO(v2): SBOM special case, to be removed after PURL to ESI conversion within each extractor is complete
	if pkg.purlCache != nil {
		return pkg.purlCache.Version
	}

	// Assume Go stdlib patch version as the latest version
	//
	// This is done because go1.20 and earlier do not support patch
	// version in go.mod file, and will fail to build.
	//
	// However, if we assume patch version as .0, this will cause a lot of
	// false positives. This compromise still allows osv-scanner to pick up
	// when the user is using a minor version that is out-of-support.
	if pkg.Ecosystem().Ecosystem == osvschema.EcosystemGo && pkg.Name() == "stdlib" {
		v := semverlike.ParseSemverLikeVersion(pkg.Inventory.Version, 3)
		if len(v.Components) == 2 {
			return fmt.Sprintf(
				"%d.%d.%d",
				v.Components.Fetch(0),
				v.Components.Fetch(1),
				99,
			)
		}
	}

	return pkg.Inventory.Version
}

func (pkg *PackageInfo) Location() string {
	if len(pkg.Inventory.Locations) > 0 {
		return pkg.Inventory.Locations[0]
	}

	return ""
}

func (pkg *PackageInfo) Commit() string {
	if pkg.Inventory.SourceCode != nil {
		return pkg.Inventory.SourceCode.Commit
	}

	return ""
}

func (pkg *PackageInfo) SourceType() models.SourceType {
	if pkg.Inventory.Extractor == nil {
		return models.SourceTypeUnknown
	}

	extractorName := pkg.Inventory.Extractor.Name()
	if _, ok := osExtractors[extractorName]; ok {
		return models.SourceTypeOSPackage
	} else if _, ok := sbomExtractors[extractorName]; ok {
		return models.SourceTypeSBOM
	} else if _, ok := gitExtractors[extractorName]; ok {
		return models.SourceTypeGit
	} else if _, ok := artifactExtractors[extractorName]; ok {
		return models.SourceTypeArtifact
	}

	return models.SourceTypeProjectPackage
}

func (pkg *PackageInfo) DepGroups() []string {
	if dg, ok := pkg.Inventory.Metadata.(scalibrosv.DepGroups); ok {
		return dg.DepGroups()
	}

	return []string{}
}

func (pkg *PackageInfo) OSPackageName() string {
	if metadata, ok := pkg.Inventory.Metadata.(*apk.Metadata); ok {
		return metadata.PackageName
	}
	if metadata, ok := pkg.Inventory.Metadata.(*dpkg.Metadata); ok {
		return metadata.PackageName
	}
	if metadata, ok := pkg.Inventory.Metadata.(*rpm.Metadata); ok {
		return metadata.PackageName
	}

	return ""
}

// FromInventory converts an extractor.Inventory into a PackageInfo.
func FromInventory(inventory *extractor.Inventory) PackageInfo {
	pi := PackageInfo{Inventory: inventory}
	if pi.SourceType() == models.SourceTypeSBOM {
		purlStruct := pi.Inventory.Extractor.ToPURL(pi.Inventory)
		if purlStruct != nil {
			purlCache, _ := purl.ToPackage(purlStruct.String())
			pi.purlCache = &purlCache
		}
	}

	return pi
}

// PackageScanResult represents a package and its associated vulnerabilities and licenses.
// This struct is used to store the results of a scan at a per package level.
type PackageScanResult struct {
	PackageInfo PackageInfo
	// TODO: Use osvschema.Vulnerability instead
	Vulnerabilities []*osvschema.Vulnerability
	Licenses        []models.License
	LayerDetails    *extractor.LayerDetails

	// TODO(v2):
	// SourceAnalysis *SourceAnalysis
	// Any additional scan enrichment steps
}
