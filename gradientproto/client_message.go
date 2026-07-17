package gradientproto

import "nix-builder-provisioner/gradientproto/wire"

// clientMessageSize and clientMessageAlign are ClientMessage's measured
// total fixed size/alignment (tag + union of all 28 variants) - see
// testdata/gen-fixtures/README.md. Only the variants this worker needs to
// send (the build capability subset) are implemented; the union size is
// still governed by the type's largest variant across all 28, so it's
// measured, not derived from just the ones used here.
const (
	clientMessageSize  = 160
	clientMessageAlign = 8 // WorkerCapabilities/other variants carry u64 fields
)

// 0-indexed declaration order from
// `enum ClientMessage { InitConnection{..}, AuthResponse{..}, ReauthRequest,
// Reject{..}, WorkerCapabilities{..}, WorkerMetrics{..}, RequestJobList,
// RequestJobChunk{..}, AssignJobResponse{..}, JobUpdate{..}, JobCompleted{..},
// JobFailed{..}, Draining, LogChunk{..}, NarRequest{..}, NarPush{..},
// NarUploaded{..}, NarStreamHeader{..}, NarRequestResume{..},
// EvalCachePull{..}, EvalCachePush{..}, EvalCacheChunk{..},
// EvalCachePushDone{..}, RequestJob{..}, RequestAllCandidates, CacheQuery{..},
// EvalMessage{..}, QueryKnownDerivations{..} }`.
const (
	clientTagInitConnection     uint32 = 0
	clientTagAuthResponse       uint32 = 1
	clientTagAssignJobResponse  uint32 = 8
	clientTagJobUpdate          uint32 = 9
	clientTagJobCompleted       uint32 = 10
	clientTagJobFailed          uint32 = 11
	clientTagDraining           uint32 = 12
	clientTagLogChunk           uint32 = 13
	clientTagNarPush            uint32 = 15
	clientTagNarUploaded        uint32 = 16
	clientTagNarStreamHeader    uint32 = 17
	clientTagWorkerCapabilities uint32 = 4
	clientTagRequestJob         uint32 = 23
	clientTagCacheQuery         uint32 = 25
)

// encodeClientMessage runs fields (already prepared, offsets relative to
// the variant's own TagField()-based Layout) through EncodeRoot with
// ClientMessage's measured size/alignment.
func encodeClientMessage(a *wire.Arena, tag uint32, fields wire.FieldWriter) []byte {
	return wire.EncodeRoot(a, clientMessageAlign, clientMessageSize, tag, fields)
}

// EncodeInitConnection encodes ClientMessage::InitConnection, the first
// message on every connection.
func EncodeInitConnection(version uint16, capabilities GradientCapabilities, id string) []byte {
	l := wire.TagField()
	versionOff := l.Field(2, 2)
	capsOff := l.Field(1, capabilitiesSize)
	idOff := l.Field(wire.StringAlign, wire.StringSize)

	a := wire.NewArena()
	versionField := wire.PrepareU16(a, version)
	capsField := prepareCapabilities(a, capabilities)
	idField := wire.PrepareString(a, id)

	fields := wire.PrepareStruct(
		wire.Field{Offset: versionOff, Write: versionField},
		wire.Field{Offset: capsOff, Write: capsField},
		wire.Field{Offset: idOff, Write: idField},
	)
	return encodeClientMessage(a, clientTagInitConnection, fields)
}

// EncodeAuthResponse encodes ClientMessage::AuthResponse: plaintext tokens
// for each peer the worker has credentials for, keyed by peer ID (or "*"
// for a wildcard token - see services.gradient.worker.peersFile).
func EncodeAuthResponse(tokens []struct{ PeerID, Token string }) []byte {
	l := wire.TagField()
	tokensOff := l.Field(wire.VecAlign, wire.VecSize)

	a := wire.NewArena()
	const tupleSize = 2 * wire.StringSize
	tokensField := wire.PrepareVec(a, len(tokens), wire.StringAlign, tupleSize, func(index int) wire.FieldWriter {
		peerIDField := wire.PrepareString(a, tokens[index].PeerID)
		tokenField := wire.PrepareString(a, tokens[index].Token)
		return wire.PrepareStruct(
			wire.Field{Offset: 0, Write: peerIDField},
			wire.Field{Offset: wire.StringSize, Write: tokenField},
		)
	})

	fields := wire.PrepareStruct(
		wire.Field{Offset: tokensOff, Write: tokensField},
	)
	return encodeClientMessage(a, clientTagAuthResponse, fields)
}

// EncodeDraining encodes ClientMessage::Draining.
func EncodeDraining() []byte {
	a := wire.NewArena()
	return encodeClientMessage(a, clientTagDraining, wire.PrepareStruct())
}

// EncodeWorkerCapabilities encodes ClientMessage::WorkerCapabilities,
// advertising build capacity after a successful handshake.
func EncodeWorkerCapabilities(architectures, systemFeatures []string, maxConcurrentBuilds, cpuCount uint32, ramTotalMB uint64, cpuCoreScore uint32) []byte {
	l := wire.TagField()
	archOff := l.Field(wire.VecAlign, wire.VecSize)
	featOff := l.Field(wire.VecAlign, wire.VecSize)
	maxBuildsOff := l.Field(4, 4)
	cpuCountOff := l.Field(4, 4)
	ramOff := l.Field(8, 8)
	scoreOff := l.Field(4, 4)

	a := wire.NewArena()
	archField := prepareStringVec(a, architectures)
	featField := prepareStringVec(a, systemFeatures)
	maxBuildsField := wire.PrepareU32(a, maxConcurrentBuilds)
	cpuCountField := wire.PrepareU32(a, cpuCount)
	ramField := wire.PrepareU64(a, ramTotalMB)
	scoreField := wire.PrepareU32(a, cpuCoreScore)

	fields := wire.PrepareStruct(
		wire.Field{Offset: archOff, Write: archField},
		wire.Field{Offset: featOff, Write: featField},
		wire.Field{Offset: maxBuildsOff, Write: maxBuildsField},
		wire.Field{Offset: cpuCountOff, Write: cpuCountField},
		wire.Field{Offset: ramOff, Write: ramField},
		wire.Field{Offset: scoreOff, Write: scoreField},
	)
	return encodeClientMessage(a, clientTagWorkerCapabilities, fields)
}

// prepareStringVec sequences a []string vec's elements via PrepareVec,
// each element prepared in index order (matching Vec<T>::serialize_from_iter -
// see wire/vec.go's PrepareVec doc).
func prepareStringVec(a *wire.Arena, values []string) wire.FieldWriter {
	return wire.PrepareVec(a, len(values), wire.StringAlign, wire.StringSize, func(index int) wire.FieldWriter {
		return wire.PrepareString(a, values[index])
	})
}

// JobKind mirrors gradient_types::proto::JobKind (fieldless: Flake=0, Build=1).
type JobKind uint8

const (
	JobKindFlake JobKind = 0
	JobKindBuild JobKind = 1
)

// EncodeRequestJob encodes ClientMessage::RequestJob{kind}: a pull-based
// capacity signal sent once per free slot after the handshake, and again
// after each AssignJob while capacity remains.
func EncodeRequestJob(kind JobKind) []byte {
	l := wire.TagField()
	kindOff := l.Field(1, 1)

	a := wire.NewArena()
	kindField := wire.PrepareTag1(a, uint8(kind))

	fields := wire.PrepareStruct(
		wire.Field{Offset: kindOff, Write: kindField},
	)
	return encodeClientMessage(a, clientTagRequestJob, fields)
}

// EncodeAssignJobResponse encodes ClientMessage::AssignJobResponse: the
// worker's accept/reject reply to a ServerMessage::AssignJob, required
// before starting work. reason should be empty when accepted is true.
func EncodeAssignJobResponse(jobID string, accepted bool, reason string) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	acceptedOff := l.Field(1, 1)
	reasonOff := l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	acceptedField := wire.PrepareBool(a, accepted)
	reasonField := wire.PrepareOptionNone()
	if !accepted {
		reasonField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, reason))
	}

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: acceptedOff, Write: acceptedField},
		wire.Field{Offset: reasonOff, Write: reasonField},
	)
	return encodeClientMessage(a, clientTagAssignJobResponse, fields)
}

// jobUpdateFieldsOff is the offset layout for ClientMessage::JobUpdate's
// two fields (job_id, update); JobUpdateKind's own tag+fields (built by
// prepareJobUpdateBuilding/prepareJobUpdateBuildOutput) are embedded
// in-line at the `Update` offset, not out-of-line - it's a plain enum
// value, not a pointer to one.
var jobUpdateFieldsOff struct {
	JobID, Update int
}

// jobUpdateKindAlign is JobUpdateKind's own measured alignment - 8, not
// the tag's 1 byte, because one of its variants (BuildOutput) contains an
// Option<BuildMetrics> and BuildMetrics has a u64 field. An enum-with-data
// type's alignment is the max alignment across every variant's fields, not
// just the active one - see TagField's doc for the related tag-width bug
// this is easy to conflate with (this is a separate concern: how a value
// of this type must itself be *positioned* when embedded as a field,
// rather than how its own internal fields are laid out).
const jobUpdateKindAlign = 8

func init() {
	l := wire.TagField()
	jobUpdateFieldsOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	jobUpdateFieldsOff.Update = l.Field(jobUpdateKindAlign, jobUpdateKindSize)
}

// EncodeJobUpdateBuilding encodes
// ClientMessage::JobUpdate{job_id, update: JobUpdateKind::Building{build_id}}.
func EncodeJobUpdateBuilding(jobID, buildID string) []byte {
	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	updateField := prepareJobUpdateBuilding(a, buildID)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobUpdateFieldsOff.JobID, Write: jobIDField},
		wire.Field{Offset: jobUpdateFieldsOff.Update, Write: updateField},
	)
	return encodeClientMessage(a, clientTagJobUpdate, fields)
}

// EncodeJobUpdateBuildOutput encodes ClientMessage::JobUpdate{job_id,
// update: JobUpdateKind::BuildOutput{build_id, outputs, metrics, substituted}}.
func EncodeJobUpdateBuildOutput(jobID, buildID string, outputs []BuildOutput, metrics *BuildMetrics, substituted bool) []byte {
	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	updateField := prepareJobUpdateBuildOutput(a, buildID, outputs, metrics, substituted)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobUpdateFieldsOff.JobID, Write: jobIDField},
		wire.Field{Offset: jobUpdateFieldsOff.Update, Write: updateField},
	)
	return encodeClientMessage(a, clientTagJobUpdate, fields)
}

// EncodeJobCompleted encodes ClientMessage::JobCompleted{job_id}.
func EncodeJobCompleted(jobID string) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
	)
	return encodeClientMessage(a, clientTagJobCompleted, fields)
}

// BuildFailureKind mirrors gradient_types::proto::BuildFailureKind
// (fieldless, drives the scheduler's retry decision).
type BuildFailureKind uint8

const (
	BuildFailureTransient             BuildFailureKind = 0
	BuildFailurePermanent             BuildFailureKind = 1
	BuildFailureTimeout               BuildFailureKind = 2
	BuildFailureSubstituteUnavailable BuildFailureKind = 3
	BuildFailureInputsUnavailable     BuildFailureKind = 4
	BuildFailureCorruptEvalCache      BuildFailureKind = 5
)

// EncodeJobFailed encodes ClientMessage::JobFailed{job_id, error, kind,
// missing_paths}. missing_paths is only meaningful for
// BuildFailureInputsUnavailable; pass nil otherwise.
func EncodeJobFailed(jobID, errMsg string, kind BuildFailureKind, missingPaths []string) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	errorOff := l.Field(wire.StringAlign, wire.StringSize)
	kindOff := l.Field(1, 1)
	missingOff := l.Field(wire.VecAlign, wire.VecSize)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	errorField := wire.PrepareString(a, errMsg)
	kindField := wire.PrepareTag1(a, uint8(kind))
	missingField := prepareStringVec(a, missingPaths)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: errorOff, Write: errorField},
		wire.Field{Offset: kindOff, Write: kindField},
		wire.Field{Offset: missingOff, Write: missingField},
	)
	return encodeClientMessage(a, clientTagJobFailed, fields)
}

// EncodeLogChunk encodes ClientMessage::LogChunk{job_id, task_index, data}.
// Fire-and-forget build log lines from an in-flight task.
func EncodeLogChunk(jobID string, taskIndex uint32, data []byte) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	taskIndexOff := l.Field(4, 4)
	dataOff := l.Field(wire.VecAlign, wire.VecSize)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	taskIndexField := wire.PrepareU32(a, taskIndex)
	dataField := prepareByteVec(a, data)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: taskIndexOff, Write: taskIndexField},
		wire.Field{Offset: dataOff, Write: dataField},
	)
	return encodeClientMessage(a, clientTagLogChunk, fields)
}

// prepareByteVec sequences a []byte's Vec<u8> representation. Byte
// elements have no out-of-line content of their own (align 1, size 1), so
// element order can't desync sibling fields the way it can for composite
// elements - but PrepareVec is still used unconditionally for consistency
// and correct empty-vec handling (see wire/vec.go's doc).
func prepareByteVec(a *wire.Arena, data []byte) wire.FieldWriter {
	return wire.PrepareVec(a, len(data), 1, 1, func(index int) wire.FieldWriter {
		b := data[index]
		return func(pos int) { wire.PutTag1(a, pos, b) }
	})
}

// EncodeNarStreamHeader encodes ClientMessage::NarStreamHeader{job_id,
// store_path, total_bytes, stream_token}: opens a push stream for
// store_path before the first NarPush.
func EncodeNarStreamHeader(jobID, storePath string, totalBytes *uint64, streamToken string) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	storePathOff := l.Field(wire.StringAlign, wire.StringSize)
	totalBytesOff := l.Field(8, wire.OptionSize(8, 8))
	tokenOff := l.Field(wire.StringAlign, wire.StringSize)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	storePathField := wire.PrepareString(a, storePath)
	totalBytesField := prepareOptU64(a, totalBytes)
	tokenField := wire.PrepareString(a, streamToken)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: storePathOff, Write: storePathField},
		wire.Field{Offset: totalBytesOff, Write: totalBytesField},
		wire.Field{Offset: tokenOff, Write: tokenField},
	)
	return encodeClientMessage(a, clientTagNarStreamHeader, fields)
}

// EncodeNarPush encodes ClientMessage::NarPush{job_id, store_path, data,
// offset, is_final}: one chunk of a NAR being pushed to the server.
func EncodeNarPush(jobID, storePath string, data []byte, offset uint64, isFinal bool) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	storePathOff := l.Field(wire.StringAlign, wire.StringSize)
	dataOff := l.Field(wire.VecAlign, wire.VecSize)
	offsetOff := l.Field(8, 8)
	isFinalOff := l.Field(1, 1)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	storePathField := wire.PrepareString(a, storePath)
	dataField := prepareByteVec(a, data)
	offsetField := wire.PrepareU64(a, offset)
	isFinalField := wire.PrepareBool(a, isFinal)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: storePathOff, Write: storePathField},
		wire.Field{Offset: dataOff, Write: dataField},
		wire.Field{Offset: offsetOff, Write: offsetField},
		wire.Field{Offset: isFinalOff, Write: isFinalField},
	)
	return encodeClientMessage(a, clientTagNarPush, fields)
}

// EncodeNarUploaded encodes ClientMessage::NarUploaded: reports metadata
// after a NAR upload (via NarPush or presigned S3) completes.
func EncodeNarUploaded(jobID, storePath, fileHash string, fileSize, narSize uint64, narHash string, references []string, deriver, ca *string) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	storePathOff := l.Field(wire.StringAlign, wire.StringSize)
	fileHashOff := l.Field(wire.StringAlign, wire.StringSize)
	fileSizeOff := l.Field(8, 8)
	narSizeOff := l.Field(8, 8)
	narHashOff := l.Field(wire.StringAlign, wire.StringSize)
	referencesOff := l.Field(wire.VecAlign, wire.VecSize)
	deriverOff := l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	caOff := l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	storePathField := wire.PrepareString(a, storePath)
	fileHashField := wire.PrepareString(a, fileHash)
	fileSizeField := wire.PrepareU64(a, fileSize)
	narSizeField := wire.PrepareU64(a, narSize)
	narHashField := wire.PrepareString(a, narHash)
	referencesField := prepareStringVec(a, references)
	deriverField := wire.PrepareOptionNone()
	if deriver != nil {
		deriverField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *deriver))
	}
	caField := wire.PrepareOptionNone()
	if ca != nil {
		caField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *ca))
	}

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: storePathOff, Write: storePathField},
		wire.Field{Offset: fileHashOff, Write: fileHashField},
		wire.Field{Offset: fileSizeOff, Write: fileSizeField},
		wire.Field{Offset: narSizeOff, Write: narSizeField},
		wire.Field{Offset: narHashOff, Write: narHashField},
		wire.Field{Offset: referencesOff, Write: referencesField},
		wire.Field{Offset: deriverOff, Write: deriverField},
		wire.Field{Offset: caOff, Write: caField},
	)
	return encodeClientMessage(a, clientTagNarUploaded, fields)
}

// QueryMode mirrors gradient_types::proto::QueryMode (fieldless:
// Normal=0, Pull=1, Push=2).
type QueryMode uint8

const (
	QueryModeNormal QueryMode = 0
	QueryModePull   QueryMode = 1
	QueryModePush   QueryMode = 2
)

// EncodeCacheQuery encodes ClientMessage::CacheQuery{job_id, query_id,
// paths, mode}. Used with QueryModePush before uploading a build output,
// to learn whether the server offers a presigned S3 PUT URL.
func EncodeCacheQuery(jobID, queryID string, paths []string, mode QueryMode) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	queryIDOff := l.Field(wire.StringAlign, wire.StringSize)
	pathsOff := l.Field(wire.VecAlign, wire.VecSize)
	modeOff := l.Field(1, 1)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)
	queryIDField := wire.PrepareString(a, queryID)
	pathsField := prepareStringVec(a, paths)
	modeField := wire.PrepareTag1(a, uint8(mode))

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: queryIDOff, Write: queryIDField},
		wire.Field{Offset: pathsOff, Write: pathsField},
		wire.Field{Offset: modeOff, Write: modeField},
	)
	return encodeClientMessage(a, clientTagCacheQuery, fields)
}
