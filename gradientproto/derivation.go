package gradientproto

import (
	"context"
	"fmt"
	"path"
	"strings"

	"nix-builder-provisioner/nixproto"
)

// nixDerivationShow is the JSON schema of `nix derivation show --json`
// (and --recursive) as emitted by Nix 2.34 - verified empirically against
// a locally-built nix binary rather than assumed from documentation, since
// this schema (the "version": 4 derivation JSON format) differs
// significantly from older Nix's format: derivations are keyed by
// basename (not full store path), inputs live under "inputs":
// {"drvs": {...}, "srcs": [...]} rather than top-level "inputDrvs"/
// "inputSrcs", and each output's resolved store path (when
// input-addressed) is a bare basename under "outputs"."<name>"."path".
//
// A content-addressed output has "hash"/"method" instead of "path" - see
// readDerivation's doc for how (and how far) this is handled.
type nixDerivationShow struct {
	Derivations map[string]nixDerivationEntry `json:"derivations"`
}

type nixDerivationEntry struct {
	Args    []string                  `json:"args"`
	Builder string                    `json:"builder"`
	Env     map[string]string         `json:"env"`
	Inputs  nixDerivationInputs       `json:"inputs"`
	Outputs map[string]nixOutputEntry `json:"outputs"`
	System  string                    `json:"system"`
}

type nixDerivationInputs struct {
	Drvs map[string]nixInputDrv `json:"drvs"`
	Srcs []string               `json:"srcs"`
}

type nixInputDrv struct {
	Outputs []string `json:"outputs"`
}

type nixOutputEntry struct {
	// Path is a bare basename (no /nix/store/ prefix), present for
	// input-addressed outputs.
	Path string `json:"path,omitempty"`
	// Hash/Method are present instead of Path for content-addressed
	// outputs (Hash in SRI form, e.g. "sha256-AAAA...=").
	Hash   string `json:"hash,omitempty"`
	Method string `json:"method,omitempty"`
}

// storeDir is hardcoded rather than queried, matching every other
// assumption already baked into this codebase (nixproto's wire-format
// helpers, StorePathBasename, etc. all assume the standard /nix/store
// prefix). A store with a nonstandard prefix isn't supported anywhere in
// this proxy today.
const storeDir = "/nix/store"

// readDerivation reads drvPath's content from a Nix store reachable via
// exec (the store host connection, for the primary use in executor.go -
// this needs to happen before a builder VM is even requested, since the
// derivation's own `system` field determines which architecture pool to
// draw from) and returns it as a nixproto.BasicDerivation ready to hand to
// nixproto.ExecuteBuild, plus the derivation's system string.
//
// Uses a single `nix derivation show --recursive` call rather than
// resolving each input derivation's output path with a separate
// round-trip: --recursive returns every transitively-referenced input
// derivation's own entry (each with its own resolved "outputs.<name>.path")
// in the same flat "derivations" map, keyed by basename - see this file's
// package-level doc comment for the schema this was verified against.
//
// Limitation: an input derivation whose requested output is
// content-addressed (no "path" field, only "hash"/"method") cannot have
// its resolved store path derived from this JSON alone - doing so
// correctly requires querying the store's realisation database (the same
// DrvOutput -> Realisation mapping nixproto/derivation.go's
// ComputeDerivationHash/MakeDrvOutputID exist to compute for build
// *results*, not to resolve arbitrary existing inputs). This is rare in
// practice (the overwhelming majority of Nixpkgs derivations are
// input-addressed) but returns an error rather than silently producing an
// incomplete InputSrcs list.
func readDerivation(ctx context.Context, exec Execer, drvPath string) (*nixproto.BasicDerivation, string, error) {
	var show nixDerivationShow
	cmd := fmt.Sprintf("nix derivation show --recursive %s", shellQuote(drvPath))
	if err := runJSON(ctx, exec, cmd, &show); err != nil {
		return nil, "", fmt.Errorf("gradientproto: reading derivation %s: %w", drvPath, err)
	}

	base := path.Base(drvPath)
	entry, ok := show.Derivations[base]
	if !ok {
		return nil, "", fmt.Errorf("gradientproto: nix derivation show did not include %s in its output", drvPath)
	}

	drv := &nixproto.BasicDerivation{
		Outputs:   make(map[string]nixproto.DerivationOutput, len(entry.Outputs)),
		Platform:  entry.System,
		Builder:   entry.Builder,
		Args:      entry.Args,
		Env:       entry.Env,
		InputSrcs: append([]string(nil), entry.Inputs.Srcs...),
	}

	for name, out := range entry.Outputs {
		if out.Path != "" {
			drv.Outputs[name] = nixproto.DerivationOutput{Path: storeDir + "/" + out.Path}
			continue
		}
		// Content-addressed own-output: HashAlgo/Hash carried through as
		// nix-daemon protocol expects (see ReadDerivationOutput/
		// WriteDerivationOutput in nixproto/derivation.go); Path is left
		// empty, matching how the ssh-ng path already handles this
		// (BasicDerivation's doc: "Path (may be empty for floating CA)").
		algo, hash, ok := parseSRIHash(out.Hash)
		if !ok {
			return nil, "", fmt.Errorf("gradientproto: output %s of %s has unparseable hash %q", name, drvPath, out.Hash)
		}
		hashAlgo := algo
		if out.Method == "nar" {
			hashAlgo = "r:" + algo
		}
		drv.Outputs[name] = nixproto.DerivationOutput{HashAlgo: hashAlgo, Hash: hash}
	}

	for inputBase, input := range entry.Inputs.Drvs {
		inputEntry, ok := show.Derivations[inputBase]
		if !ok {
			return nil, "", fmt.Errorf("gradientproto: nix derivation show --recursive did not include input %s of %s", inputBase, drvPath)
		}
		for _, outName := range input.Outputs {
			out, ok := inputEntry.Outputs[outName]
			if !ok {
				return nil, "", fmt.Errorf("gradientproto: input %s of %s has no output %q", inputBase, drvPath, outName)
			}
			if out.Path == "" {
				return nil, "", fmt.Errorf("gradientproto: input %s of %s requests content-addressed output %q, which this worker cannot resolve to a store path yet (see readDerivation's doc)", inputBase, drvPath, outName)
			}
			drv.InputSrcs = append(drv.InputSrcs, storeDir+"/"+out.Path)
		}
	}

	return drv, entry.System, nil
}

// parseSRIHash splits an SRI-format hash string ("sha256-<base64>") into
// its algorithm and base64 payload. Nix's `nix derivation show` emits
// fixed-output hashes in this form (verified empirically - see this
// file's package doc), not the base32 form nixproto's own wire format
// uses elsewhere; nix-daemon's BuildDerivation accepts the base64/SRI form
// for HashAlgo+Hash equally well (it's what modern `nix build` itself
// sends), so no re-encoding is needed here.
func parseSRIHash(sri string) (algo, hash string, ok bool) {
	algo, hash, found := strings.Cut(sri, "-")
	if !found || algo == "" || hash == "" {
		return "", "", false
	}
	return algo, hash, true
}

// shellQuote wraps s in single quotes for safe interpolation into a shell
// command string, escaping any single quotes within it. Store paths are
// virtually never adversarial, but derivation names come from
// (indirectly) evaluated Nix expressions the worker doesn't control.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
