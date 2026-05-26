package nixproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
)

// DerivationOutput represents a single output of a derivation
type DerivationOutput struct {
	Path     string // Store path (may be empty for floating CA)
	HashAlgo string // Hash algorithm (may include method prefix like "r:sha256")
	Hash     string // Hash value (may be empty)
}

// BasicDerivation represents a derivation to be built
// This matches Nix's BasicDerivation struct
type BasicDerivation struct {
	Outputs   map[string]DerivationOutput // Output name -> output info
	InputSrcs []string                    // Input source paths
	Platform  string                      // e.g., "aarch64-linux"
	Builder   string                      // Path to the builder executable
	Args      []string                    // Arguments to the builder
	Env       map[string]string           // Environment variables
}

// ReadDerivationOutput reads a single derivation output from the wire
func ReadDerivationOutput(r io.Reader) (DerivationOutput, error) {
	path, err := ReadString(r)
	if err != nil {
		return DerivationOutput{}, fmt.Errorf("reading output path: %w", err)
	}

	hashAlgo, err := ReadString(r)
	if err != nil {
		return DerivationOutput{}, fmt.Errorf("reading hash algo: %w", err)
	}

	hash, err := ReadString(r)
	if err != nil {
		return DerivationOutput{}, fmt.Errorf("reading hash: %w", err)
	}

	return DerivationOutput{
		Path:     path,
		HashAlgo: hashAlgo,
		Hash:     hash,
	}, nil
}

// WriteDerivationOutput writes a single derivation output to the wire
func WriteDerivationOutput(w io.Writer, out DerivationOutput) error {
	if err := WriteString(w, out.Path); err != nil {
		return err
	}
	if err := WriteString(w, out.HashAlgo); err != nil {
		return err
	}
	return WriteString(w, out.Hash)
}

// ReadBasicDerivation reads a BasicDerivation from the wire format
// This matches the format used in daemon.cc's readDerivation
func ReadBasicDerivation(r io.Reader) (*BasicDerivation, error) {
	drv := &BasicDerivation{
		Outputs: make(map[string]DerivationOutput),
		Env:     make(map[string]string),
	}

	// Read outputs
	outputCount, err := ReadUint64(r)
	if err != nil {
		return nil, fmt.Errorf("reading output count: %w", err)
	}

	for i := uint64(0); i < outputCount; i++ {
		name, err := ReadString(r)
		if err != nil {
			return nil, fmt.Errorf("reading output name: %w", err)
		}

		output, err := ReadDerivationOutput(r)
		if err != nil {
			return nil, fmt.Errorf("reading output %s: %w", name, err)
		}

		drv.Outputs[name] = output
	}

	// Read input sources (StorePathSet)
	drv.InputSrcs, err = ReadStrings(r)
	if err != nil {
		return nil, fmt.Errorf("reading input sources: %w", err)
	}

	// Read platform
	drv.Platform, err = ReadString(r)
	if err != nil {
		return nil, fmt.Errorf("reading platform: %w", err)
	}

	// Read builder
	drv.Builder, err = ReadString(r)
	if err != nil {
		return nil, fmt.Errorf("reading builder: %w", err)
	}

	// Read args
	drv.Args, err = ReadStrings(r)
	if err != nil {
		return nil, fmt.Errorf("reading args: %w", err)
	}

	// Read environment
	envCount, err := ReadUint64(r)
	if err != nil {
		return nil, fmt.Errorf("reading env count: %w", err)
	}

	for i := uint64(0); i < envCount; i++ {
		key, err := ReadString(r)
		if err != nil {
			return nil, fmt.Errorf("reading env key: %w", err)
		}
		value, err := ReadString(r)
		if err != nil {
			return nil, fmt.Errorf("reading env value: %w", err)
		}
		drv.Env[key] = value
	}

	return drv, nil
}

// WriteBasicDerivation writes a BasicDerivation to the wire format
func WriteBasicDerivation(w io.Writer, drv *BasicDerivation) error {
	// Write outputs
	if err := WriteUint64(w, uint64(len(drv.Outputs))); err != nil {
		return err
	}
	for name, output := range drv.Outputs {
		if err := WriteString(w, name); err != nil {
			return err
		}
		if err := WriteDerivationOutput(w, output); err != nil {
			return err
		}
	}

	// Write input sources
	if err := WriteStrings(w, drv.InputSrcs); err != nil {
		return err
	}

	// Write platform
	if err := WriteString(w, drv.Platform); err != nil {
		return err
	}

	// Write builder
	if err := WriteString(w, drv.Builder); err != nil {
		return err
	}

	// Write args
	if err := WriteStrings(w, drv.Args); err != nil {
		return err
	}

	// Write environment
	if err := WriteUint64(w, uint64(len(drv.Env))); err != nil {
		return err
	}
	for k, v := range drv.Env {
		if err := WriteString(w, k); err != nil {
			return err
		}
		if err := WriteString(w, v); err != nil {
			return err
		}
	}

	return nil
}

// BuildResult represents the result of a build operation
type BuildResult struct {
	Status             BuildResultStatus
	ErrorMsg           string
	TimesBuilt         uint64
	IsNonDeterministic bool
	StartTime          uint64                 // Unix timestamp (may be 0)
	StopTime           uint64                 // Unix timestamp (may be 0)
	CpuUser            *uint64                // CPU user time in microseconds (nil if not reported)
	CpuSystem          *uint64                // CPU system time in microseconds (nil if not reported)
	BuiltOutputs       map[string]Realisation // output name -> realisation
}

// BuildResultStatus represents the status of a build
type BuildResultStatus uint64

const (
	BuildResultBuilt                  BuildResultStatus = 0
	BuildResultSubstituted            BuildResultStatus = 1
	BuildResultAlreadyValid           BuildResultStatus = 2
	BuildResultPermanentFailure       BuildResultStatus = 3
	BuildResultInputRejected          BuildResultStatus = 4
	BuildResultOutputRejected         BuildResultStatus = 5
	BuildResultTransientFailure       BuildResultStatus = 6
	BuildResultCachedFailure          BuildResultStatus = 7
	BuildResultTimedOut               BuildResultStatus = 8
	BuildResultMiscFailure            BuildResultStatus = 9
	BuildResultDependencyFailed       BuildResultStatus = 10
	BuildResultLogLimitExceeded       BuildResultStatus = 11
	BuildResultNotDeterministic       BuildResultStatus = 12
	BuildResultResolvesToAlreadyValid BuildResultStatus = 13
	BuildResultNoSubstituters         BuildResultStatus = 14
)

// Realisation represents a realized derivation output
type Realisation struct {
	ID                    string            `json:"id"`
	OutPath               string            `json:"outPath"`
	Signatures            []string          `json:"signatures"`
	DependentRealisations map[string]string `json:"dependentRealisations"`
}

// ReadBuildResult reads a BuildResult from the wire
func ReadBuildResult(r io.Reader, protoVersion uint64) (*BuildResult, error) {
	result := &BuildResult{
		BuiltOutputs: make(map[string]Realisation),
	}

	status, err := ReadUint64(r)
	if err != nil {
		return nil, fmt.Errorf("reading status: %w", err)
	}
	result.Status = BuildResultStatus(status)

	result.ErrorMsg, err = ReadString(r)
	if err != nil {
		return nil, fmt.Errorf("reading error msg: %w", err)
	}

	// Protocol version >= 1.29 includes additional fields
	minor := protoVersion & 0xff
	if minor >= 29 {
		result.TimesBuilt, err = ReadUint64(r)
		if err != nil {
			return nil, fmt.Errorf("reading timesBuilt: %w", err)
		}

		isNonDet, err := ReadBool(r)
		if err != nil {
			return nil, fmt.Errorf("reading isNonDeterministic: %w", err)
		}
		result.IsNonDeterministic = isNonDet
	}

	// Protocol version >= 1.37 includes timing fields
	if minor >= 37 {
		result.StartTime, err = ReadUint64(r)
		if err != nil {
			return nil, fmt.Errorf("reading startTime: %w", err)
		}

		result.StopTime, err = ReadUint64(r)
		if err != nil {
			return nil, fmt.Errorf("reading stopTime: %w", err)
		}

		// cpuUser: optional<microseconds>
		tag, err := ReadUint64(r)
		if err != nil {
			return nil, fmt.Errorf("reading cpuUser tag: %w", err)
		}
		if tag != 0 {
			val, err := ReadUint64(r)
			if err != nil {
				return nil, fmt.Errorf("reading cpuUser value: %w", err)
			}
			result.CpuUser = &val
		}

		// cpuSystem: optional<microseconds>
		tag, err = ReadUint64(r)
		if err != nil {
			return nil, fmt.Errorf("reading cpuSystem tag: %w", err)
		}
		if tag != 0 {
			val, err := ReadUint64(r)
			if err != nil {
				return nil, fmt.Errorf("reading cpuSystem value: %w", err)
			}
			result.CpuSystem = &val
		}
	}

	// Protocol version >= 1.28 includes builtOutputs
	// For protocol 1.35+, Realisations are serialized as JSON strings
	if minor >= 28 {
		count, err := ReadUint64(r)
		if err != nil {
			return nil, fmt.Errorf("reading builtOutputs count: %w", err)
		}

		for i := uint64(0); i < count; i++ {
			// Read DrvOutput ID (the key)
			id, err := ReadString(r)
			if err != nil {
				return nil, fmt.Errorf("reading realisation id: %w", err)
			}

			// Read Realisation as JSON string (the value)
			jsonStr, err := ReadString(r)
			if err != nil {
				return nil, fmt.Errorf("reading realisation JSON: %w", err)
			}

			var real Realisation
			if err := json.Unmarshal([]byte(jsonStr), &real); err != nil {
				return nil, fmt.Errorf("unmarshaling realisation JSON: %w", err)
			}

			// Ensure non-nil collections to avoid null in JSON output
			if real.Signatures == nil {
				real.Signatures = []string{}
			}
			if real.DependentRealisations == nil {
				real.DependentRealisations = make(map[string]string)
			}

			result.BuiltOutputs[id] = real
		}
	}

	return result, nil
}

// ComputeDerivationHash computes the SHA256 hash of a derivation in ATerm format
// This is used to construct DrvOutput IDs for input-addressed derivations
func ComputeDerivationHash(drv *BasicDerivation) string {
	aterm := SerializeToATerm(drv)
	hash := sha256.Sum256([]byte(aterm))
	return hex.EncodeToString(hash[:])
}

// SerializeToATerm serializes a BasicDerivation to Nix ATerm format
// Format: Derive([outputs],[inputDrvs],[inputSrcs],"platform","builder",[args],[env])
func SerializeToATerm(drv *BasicDerivation) string {
	var sb strings.Builder

	sb.WriteString("Derive([")

	// Outputs - sorted by name for determinism
	outputNames := make([]string, 0, len(drv.Outputs))
	for name := range drv.Outputs {
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)

	for i, name := range outputNames {
		if i > 0 {
			sb.WriteString(",")
		}
		out := drv.Outputs[name]
		sb.WriteString("(")
		writeATString(&sb, name)
		sb.WriteString(",")
		writeATString(&sb, out.Path)
		sb.WriteString(",")
		writeATString(&sb, out.HashAlgo)
		sb.WriteString(",")
		writeATString(&sb, out.Hash)
		sb.WriteString(")")
	}

	sb.WriteString("],[")

	// InputDrvs - for BasicDerivation this is empty (inputDrvs are resolved to inputSrcs)
	// In the wire protocol, BasicDerivation doesn't include inputDrvs

	sb.WriteString("],[")

	// InputSrcs - sorted for determinism
	inputSrcs := make([]string, len(drv.InputSrcs))
	copy(inputSrcs, drv.InputSrcs)
	sort.Strings(inputSrcs)

	for i, src := range inputSrcs {
		if i > 0 {
			sb.WriteString(",")
		}
		writeATString(&sb, src)
	}

	sb.WriteString("],")

	// Platform
	writeATString(&sb, drv.Platform)
	sb.WriteString(",")

	// Builder
	writeATString(&sb, drv.Builder)
	sb.WriteString(",[")

	// Args
	for i, arg := range drv.Args {
		if i > 0 {
			sb.WriteString(",")
		}
		writeATString(&sb, arg)
	}

	sb.WriteString("],[")

	// Environment - sorted by key for determinism
	envKeys := make([]string, 0, len(drv.Env))
	for k := range drv.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	for i, k := range envKeys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(")
		writeATString(&sb, k)
		sb.WriteString(",")
		writeATString(&sb, drv.Env[k])
		sb.WriteString(")")
	}

	sb.WriteString("])")

	return sb.String()
}

// writeATString writes a string in ATerm format (with escaping)
func writeATString(sb *strings.Builder, s string) {
	sb.WriteString("\"")
	for _, c := range s {
		switch c {
		case '\\':
			sb.WriteString("\\\\")
		case '"':
			sb.WriteString("\\\"")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		default:
			sb.WriteRune(c)
		}
	}
	sb.WriteString("\"")
}

func WriteBuildResult(w io.Writer, result *BuildResult, protoVersion uint64) error {
	minor := protoVersion & 0xff

	log.Printf("DEBUG WriteBuildResult: protoVersion=%d.%d, status=%d, errorMsg=%q, timesBuilt=%d, outputs=%d",
		(protoVersion>>8)&0xff, minor, result.Status, result.ErrorMsg, result.TimesBuilt, len(result.BuiltOutputs))

	// 1. Status
	if err := WriteUint64(w, uint64(result.Status)); err != nil {
		return err
	}

	// 2. Error Message - ALWAYS written (no version check!)
	if err := WriteString(w, result.ErrorMsg); err != nil {
		return err
	}

	// 3. Metadata (v1.29+)
	if minor >= 29 {
		if err := WriteUint64(w, result.TimesBuilt); err != nil {
			return err
		}
		if err := WriteBool(w, result.IsNonDeterministic); err != nil {
			return err
		}
	}

	// 4. Timings (v1.37+)
	if minor >= 37 {
		if err := WriteUint64(w, result.StartTime); err != nil {
			return err
		}
		if err := WriteUint64(w, result.StopTime); err != nil {
			return err
		}
	}

	// 5. CPU time (v1.37+)
	if minor >= 37 {
		// cpuUser: optional<microseconds>
		if result.CpuUser != nil {
			if err := WriteUint64(w, 1); err != nil {
				return err
			}
			if err := WriteUint64(w, *result.CpuUser); err != nil {
				return err
			}
		} else {
			if err := WriteUint64(w, 0); err != nil {
				return err
			}
		}
		// cpuSystem: optional<microseconds>
		if result.CpuSystem != nil {
			if err := WriteUint64(w, 1); err != nil {
				return err
			}
			if err := WriteUint64(w, *result.CpuSystem); err != nil {
				return err
			}
		} else {
			if err := WriteUint64(w, 0); err != nil {
				return err
			}
		}
	}

	// 6. Built Outputs (v1.28+) - Written LAST in standard protocol
	// For protocol 1.35+, Realisations are serialized as JSON strings
	if minor >= 28 {
		if err := WriteUint64(w, uint64(len(result.BuiltOutputs))); err != nil {
			return err
		}

		for id, real := range result.BuiltOutputs {
			// Write the key (DrvOutput ID)
			log.Printf("DEBUG WriteBuildResult: writing output key=%q", id)
			if err := WriteString(w, id); err != nil {
				return err
			}

			// Write the value as JSON
			jsonBytes, err := json.Marshal(real)
			if err != nil {
				return fmt.Errorf("marshaling realisation to JSON: %w", err)
			}
			log.Printf("DEBUG WriteBuildResult: writing output json=%s", string(jsonBytes))
			if err := WriteString(w, string(jsonBytes)); err != nil {
				return err
			}
		}
	}

	return nil
}
