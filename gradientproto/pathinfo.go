package gradientproto

import (
	"context"
	"fmt"
	"strings"
)

// nixPathInfoEntry is the JSON schema of one entry from `nix path-info
// --json <path>` (top-level object keyed by the full store path) - verified
// empirically against a locally-built Nix 2.34 binary (see
// testdata/nix-json/path_info_*.json, captured from real `nix path-info`
// output, which the tests in pathinfo_test.go parse directly).
type nixPathInfoEntry struct {
	CA         *string  `json:"ca"`
	Deriver    *string  `json:"deriver"`
	NarHash    string   `json:"narHash"`
	NarSize    uint64   `json:"narSize"`
	References []string `json:"references"`
	Signatures []string `json:"signatures"`
}

// pathInfo is the subset of nixPathInfoEntry's fields NarUploaded needs,
// with References already reduced to Gradient's expected bare-basename
// form (see EncodeNarUploaded's doc: "Store-path references in hash-name
// format, without /nix/store/ prefix").
type pathInfo struct {
	CA         *string
	Deriver    *string
	NarHash    string
	NarSize    uint64
	References []string
}

// queryPathInfo reads path's metadata via `nix path-info --json`, for use
// building a ClientMessage::NarUploaded after a build (or substitution)
// completes. path must already exist in the store reachable via exec -
// callers query this only after nixproto.ExecuteBuild has reported the
// output as built/substituted/already-valid.
func queryPathInfo(ctx context.Context, exec Execer, path string) (*pathInfo, error) {
	var result map[string]*nixPathInfoEntry
	cmd := fmt.Sprintf("nix path-info --json %s", shellQuote(path))
	if err := runJSON(ctx, exec, cmd, &result); err != nil {
		return nil, fmt.Errorf("gradientproto: querying path info for %s: %w", path, err)
	}

	entry, ok := result[path]
	if !ok || entry == nil {
		return nil, fmt.Errorf("gradientproto: nix path-info reported %s as missing from the store", path)
	}

	refs := make([]string, len(entry.References))
	for i, r := range entry.References {
		refs[i] = strings.TrimPrefix(r, storeDir+"/")
	}

	return &pathInfo{
		CA:         entry.CA,
		Deriver:    entry.Deriver,
		NarHash:    entry.NarHash,
		NarSize:    entry.NarSize,
		References: refs,
	}, nil
}
