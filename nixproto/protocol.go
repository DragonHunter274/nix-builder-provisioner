// Package nixproto implements the Nix daemon wire protocol.
// This allows us to intercept and route individual operations (especially BuildDerivation)
// to different backend builders.
package nixproto

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Protocol constants
const (
	// Magic numbers for handshake
	ClientMagic uint64 = 0x6e697863 // "nixc"
	ServerMagic uint64 = 0x6478696f // "dxio"

	// Current protocol version (major << 8 | minor)
	ProtocolVersionMajor = 1
	ProtocolVersionMinor = 38 // Use 1.37 to avoid cpuUser complexity in 1.38
	ProtocolVersion      = (ProtocolVersionMajor << 8) | ProtocolVersionMinor

	// Stderr markers - these are used during operation execution
	StderrNext          uint64 = 0x6f6c6d67
	StderrRead          uint64 = 0x64617461 // data needed from source
	StderrWrite         uint64 = 0x64617416 // data for sink
	StderrLast          uint64 = 0x616c7473
	StderrError         uint64 = 0x63787470
	StderrStartActivity uint64 = 0x53545254
	StderrStopActivity  uint64 = 0x53544f50
	StderrResult        uint64 = 0x52534c54
)

// WorkerOp represents a Nix daemon operation code
type WorkerOp uint64

// Worker protocol operation codes
const (
	OpIsValidPath                 WorkerOp = 1
	OpHasSubstitutes              WorkerOp = 3
	OpQueryPathHash               WorkerOp = 4 // obsolete
	OpQueryReferences             WorkerOp = 5 // obsolete
	OpQueryReferrers              WorkerOp = 6
	OpAddToStore                  WorkerOp = 7
	OpAddTextToStore              WorkerOp = 8 // obsolete
	OpBuildPaths                  WorkerOp = 9
	OpEnsurePath                  WorkerOp = 10
	OpAddTempRoot                 WorkerOp = 11
	OpAddIndirectRoot             WorkerOp = 12
	OpSyncWithGC                  WorkerOp = 13
	OpFindRoots                   WorkerOp = 14
	OpExportPath                  WorkerOp = 16 // obsolete
	OpQueryDeriver                WorkerOp = 18 // obsolete
	OpSetOptions                  WorkerOp = 19
	OpCollectGarbage              WorkerOp = 20
	OpQuerySubstitutablePathInfo  WorkerOp = 21
	OpQueryDerivationOutputs      WorkerOp = 22 // obsolete
	OpQueryAllValidPaths          WorkerOp = 23
	OpQueryFailedPaths            WorkerOp = 24
	OpClearFailedPaths            WorkerOp = 25
	OpQueryPathInfo               WorkerOp = 26
	OpImportPaths                 WorkerOp = 27 // obsolete
	OpQueryDerivationOutputNames  WorkerOp = 28 // obsolete
	OpQueryPathFromHashPart       WorkerOp = 29
	OpQuerySubstitutablePathInfos WorkerOp = 30
	OpQueryValidPaths             WorkerOp = 31
	OpQuerySubstitutablePaths     WorkerOp = 32
	OpQueryValidDerivers          WorkerOp = 33
	OpOptimiseStore               WorkerOp = 34
	OpVerifyStore                 WorkerOp = 35
	OpBuildDerivation             WorkerOp = 36
	OpAddSignatures               WorkerOp = 37
	OpNarFromPath                 WorkerOp = 38
	OpAddToStoreNar               WorkerOp = 39
	OpQueryMissing                WorkerOp = 40
	OpQueryDerivationOutputMap    WorkerOp = 41
	OpRegisterDrvOutput           WorkerOp = 42
	OpQueryRealisation            WorkerOp = 43
	OpAddMultipleToStore          WorkerOp = 44
	OpAddBuildLog                 WorkerOp = 45
	OpBuildPathsWithResults       WorkerOp = 46
	OpAddPermRoot                 WorkerOp = 47
)

func (op WorkerOp) String() string {
	names := map[WorkerOp]string{
		OpIsValidPath:                 "IsValidPath",
		OpHasSubstitutes:              "HasSubstitutes",
		OpQueryReferrers:              "QueryReferrers",
		OpAddToStore:                  "AddToStore",
		OpBuildPaths:                  "BuildPaths",
		OpEnsurePath:                  "EnsurePath",
		OpAddTempRoot:                 "AddTempRoot",
		OpAddIndirectRoot:             "AddIndirectRoot",
		OpSyncWithGC:                  "SyncWithGC",
		OpFindRoots:                   "FindRoots",
		OpSetOptions:                  "SetOptions",
		OpCollectGarbage:              "CollectGarbage",
		OpQuerySubstitutablePathInfo:  "QuerySubstitutablePathInfo",
		OpQueryAllValidPaths:          "QueryAllValidPaths",
		OpQueryFailedPaths:            "QueryFailedPaths",
		OpClearFailedPaths:            "ClearFailedPaths",
		OpQueryPathInfo:               "QueryPathInfo",
		OpQueryPathFromHashPart:       "QueryPathFromHashPart",
		OpQuerySubstitutablePathInfos: "QuerySubstitutablePathInfos",
		OpQueryValidPaths:             "QueryValidPaths",
		OpQuerySubstitutablePaths:     "QuerySubstitutablePaths",
		OpQueryValidDerivers:          "QueryValidDerivers",
		OpOptimiseStore:               "OptimiseStore",
		OpVerifyStore:                 "VerifyStore",
		OpBuildDerivation:             "BuildDerivation",
		OpAddSignatures:               "AddSignatures",
		OpNarFromPath:                 "NarFromPath",
		OpAddToStoreNar:               "AddToStoreNar",
		OpQueryMissing:                "QueryMissing",
		OpQueryDerivationOutputMap:    "QueryDerivationOutputMap",
		OpRegisterDrvOutput:           "RegisterDrvOutput",
		OpQueryRealisation:            "QueryRealisation",
		OpAddMultipleToStore:          "AddMultipleToStore",
		OpAddBuildLog:                 "AddBuildLog",
		OpBuildPathsWithResults:       "BuildPathsWithResults",
		OpAddPermRoot:                 "AddPermRoot",
	}
	if name, ok := names[op]; ok {
		return name
	}
	return fmt.Sprintf("Unknown(%d)", op)
}

// BuildMode represents the mode for building derivations
type BuildMode uint8

const (
	BuildModeNormal BuildMode = 0
	BuildModeRepair BuildMode = 1
	BuildModeCheck  BuildMode = 2
)

// Wire protocol reading functions

// ReadUint64 reads a little-endian uint64 from the reader
func ReadUint64(r io.Reader) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// WriteUint64 writes a little-endian uint64 to the writer
func WriteUint64(w io.Writer, v uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

// ReadBool reads a boolean (as uint64 0 or 1)
func ReadBool(r io.Reader) (bool, error) {
	v, err := ReadUint64(r)
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// WriteBool writes a boolean as uint64
func WriteBool(w io.Writer, v bool) error {
	var val uint64
	if v {
		val = 1
	}
	return WriteUint64(w, val)
}

// ReadBytes reads a length-prefixed byte slice with padding
func ReadBytes(r io.Reader) ([]byte, error) {
	length, err := ReadUint64(r)
	if err != nil {
		return nil, err
	}

	data := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, err
		}
	}

	// Read padding to 8-byte boundary
	padLen := (8 - (length % 8)) % 8
	if padLen > 0 {
		padding := make([]byte, padLen)
		if _, err := io.ReadFull(r, padding); err != nil {
			return nil, err
		}
	}

	return data, nil
}

// WriteBytes writes a length-prefixed byte slice with padding
func WriteBytes(w io.Writer, data []byte) error {
	if err := WriteUint64(w, uint64(len(data))); err != nil {
		return err
	}

	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}

	// Write padding to 8-byte boundary
	padLen := (8 - (len(data) % 8)) % 8
	if padLen > 0 {
		padding := make([]byte, padLen)
		if _, err := w.Write(padding); err != nil {
			return err
		}
	}

	return nil
}

// ReadString reads a length-prefixed string
func ReadString(r io.Reader) (string, error) {
	data, err := ReadBytes(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteString writes a length-prefixed string
func WriteString(w io.Writer, s string) error {
	return WriteBytes(w, []byte(s))
}

// ReadStrings reads a list of strings
func ReadStrings(r io.Reader) ([]string, error) {
	count, err := ReadUint64(r)
	if err != nil {
		return nil, err
	}

	result := make([]string, count)
	for i := uint64(0); i < count; i++ {
		s, err := ReadString(r)
		if err != nil {
			return nil, err
		}
		result[i] = s
	}
	return result, nil
}

// WriteStrings writes a list of strings
func WriteStrings(w io.Writer, ss []string) error {
	if err := WriteUint64(w, uint64(len(ss))); err != nil {
		return err
	}
	for _, s := range ss {
		if err := WriteString(w, s); err != nil {
			return err
		}
	}
	return nil
}

// ReadStringSet reads a set of strings (same wire format as list)
func ReadStringSet(r io.Reader) (map[string]struct{}, error) {
	ss, err := ReadStrings(r)
	if err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		result[s] = struct{}{}
	}
	return result, nil
}

// WriteStringSet writes a set of strings
func WriteStringSet(w io.Writer, set map[string]struct{}) error {
	ss := make([]string, 0, len(set))
	for s := range set {
		ss = append(ss, s)
	}
	return WriteStrings(w, ss)
}

// ReadStringMap reads a map of string to string
func ReadStringMap(r io.Reader) (map[string]string, error) {
	count, err := ReadUint64(r)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, count)
	for i := uint64(0); i < count; i++ {
		key, err := ReadString(r)
		if err != nil {
			return nil, err
		}
		value, err := ReadString(r)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

// WriteStringMap writes a map of string to string
func WriteStringMap(w io.Writer, m map[string]string) error {
	if err := WriteUint64(w, uint64(len(m))); err != nil {
		return err
	}
	for k, v := range m {
		if err := WriteString(w, k); err != nil {
			return err
		}
		if err := WriteString(w, v); err != nil {
			return err
		}
	}
	return nil
}

// Nix32 base32 alphabet (omits e, m, o, t, u)
const nix32Alphabet = "0123456789abcdfghijklmnpqrsvwxyz"

// nix32ToBytes decodes a nix32-encoded string to bytes
func nix32ToBytes(s string) ([]byte, error) {
	if len(s) != 32 {
		return nil, fmt.Errorf("nix32 string must be 32 chars, got %d", len(s))
	}

	// Build reverse lookup
	lookup := make(map[byte]int)
	for i, c := range []byte(nix32Alphabet) {
		lookup[c] = i
	}

	// Decode: nix32 is encoded with chars in reverse order
	var hash [20]byte
	for i := 0; i < 32; i++ {
		val, ok := lookup[s[31-i]]
		if !ok {
			return nil, fmt.Errorf("invalid nix32 char: %c", s[31-i])
		}

		bitPos := i * 5
		bytePos := bitPos / 8
		bitOffset := bitPos % 8

		if bytePos < 20 {
			hash[bytePos] |= byte(val << bitOffset)
		}
		if bitOffset > 3 && bytePos+1 < 20 {
			hash[bytePos+1] |= byte(val >> (8 - bitOffset))
		}
	}

	return hash[:], nil
}

// MakeDrvOutputID constructs a DrvOutput ID string for an input-addressed derivation
// Format: sha256:<hexHash>!<outputName>
// For input-addressed derivations, the hash is derived from the derivation path's hash part
func MakeDrvOutputID(drvPath string, outputName string) (string, error) {
	// Extract the basename (part after /nix/store/)
	basename := drvPath
	if idx := len("/nix/store/"); len(drvPath) > idx && drvPath[:idx] == "/nix/store/" {
		basename = drvPath[idx:]
	}

	// Extract hash part (first 32 chars before the first '-')
	hashPart := basename
	for i, c := range basename {
		if c == '-' {
			hashPart = basename[:i]
			break
		}
	}

	if len(hashPart) != 32 {
		return "", fmt.Errorf("invalid hash part length: %d", len(hashPart))
	}

	// Decode nix32 to bytes
	hashBytes, err := nix32ToBytes(hashPart)
	if err != nil {
		return "", fmt.Errorf("decoding nix32: %w", err)
	}

	// The DrvOutput hash for input-addressed derivations is the store path hash
	// padded to 32 bytes (SHA256 size) with zeros
	var fullHash [32]byte
	copy(fullHash[:], hashBytes)

	hexHash := fmt.Sprintf("%x", fullHash)
	return fmt.Sprintf("sha256:%s!%s", hexHash, outputName), nil
}

// StorePathBasename extracts the basename from a store path
// e.g., "/nix/store/abc123-hello" -> "abc123-hello"
func StorePathBasename(path string) string {
	if idx := len("/nix/store/"); len(path) > idx && path[:idx] == "/nix/store/" {
		return path[idx:]
	}
	return path
}
