package gradientproto

import (
	"fmt"

	"nix-builder-provisioner/gradientproto/wire"
)

// serverMessageSize and serverMessageAlign are ServerMessage's measured
// total fixed size/alignment (tag + union of all 24 variants) - see
// testdata/gen-fixtures/README.md. Only the variants this worker needs to
// receive (the build capability subset) are decoded; unrecognized tags
// decode to ServerMsgUnknown rather than erroring, so a future server
// sending a variant we don't yet implement doesn't take the connection
// down - see DecodeServerMessage.
const (
	serverMessageSize  = 112
	serverMessageAlign = 8 // AssignJob embeds a Job, which itself needs align 8
)

// 0-indexed declaration order from
// `enum ServerMessage { AuthChallenge{..}, InitAck{..}, AuthUpdate{..},
// Reject{..}, Error{..}, Draining, JobListChunk{..}, JobOffer{..},
// RevokeJob{..}, AssignJob{..}, AbortJob{..}, Credential{..}, NarPush{..},
// NarUnavailable{..}, NarAbort{..}, NarStreamHeader{..}, NarPushResume{..},
// EvalCachePullResult{..}, EvalCacheChunk{..}, EvalCachePushGrant{..},
// RequestAllScores, CacheStatus{..}, KnownDerivations{..}, CacheError{..} }`.
const (
	serverTagAuthChallenge uint32 = 0
	serverTagInitAck       uint32 = 1
	serverTagReject        uint32 = 3
	serverTagError         uint32 = 4
	serverTagDraining      uint32 = 5
	serverTagAssignJob     uint32 = 9
	serverTagAbortJob      uint32 = 10
	serverTagNarPushResume uint32 = 16
	serverTagCacheStatus   uint32 = 21
	serverTagCacheError    uint32 = 23
)

// ServerMessageKind discriminates which fields of a decoded ServerMessage
// are populated - see ServerMessage's doc.
type ServerMessageKind int

const (
	ServerMsgUnknown ServerMessageKind = iota
	ServerMsgAuthChallenge
	ServerMsgInitAck
	ServerMsgReject
	ServerMsgError
	ServerMsgDraining
	ServerMsgAssignJob
	ServerMsgAbortJob
	ServerMsgNarPushResume
	ServerMsgCacheStatus
	ServerMsgCacheError
)

// ServerMessage is a decoded message from Gradient, covering the variants
// this worker's build capability needs to handle (see
// testdata/gen-fixtures/README.md for the full 24-variant catalog this is
// a subset of). Go has no sum types, so this is deliberately a flat struct
// with all variants' fields inline rather than ~10 separate payload
// types - callers must switch on Kind first and only read the fields
// documented for that Kind; other fields are zero-valued, not meaningful.
type ServerMessage struct {
	Kind ServerMessageKind

	// ServerMsgAuthChallenge: Peers.
	// ServerMsgInitAck: AuthorizedPeers (peers already authorized before
	// this challenge - overlaps in meaning with Peers is intentional: both
	// reuse this field since InitAck has no separate "Peers" concept).
	Peers []string

	// ServerMsgInitAck.
	Version         uint16
	Capabilities    GradientCapabilities
	AuthorizedPeers []string
	FailedPeers     []FailedPeer

	// ServerMsgReject / ServerMsgError.
	Code    uint16
	Message string // Error.message, CacheError.message

	// ServerMsgReject / ServerMsgAbortJob: Reason.
	Reason string

	// ServerMsgAssignJob / ServerMsgAbortJob / ServerMsgNarPushResume: JobID.
	JobID string
	// ServerMsgAssignJob: the assigned build job (Job::Flake is rejected at
	// decode time - see DecodeJob).
	Job BuildJob

	// ServerMsgNarPushResume.
	StorePath     string
	ReceivedBytes uint64

	// ServerMsgCacheStatus / ServerMsgCacheError: QueryID.
	QueryID string
	// ServerMsgCacheStatus.
	Cached []CachedPath

	// UnknownTag is set when Kind == ServerMsgUnknown, for logging.
	UnknownTag uint32
}

var (
	authChallengeOff struct{ Peers int }
	initAckOff       struct {
		Version, Capabilities, AuthorizedPeers, FailedPeers int
	}
	rejectOff        struct{ Code, Reason int }
	errorOff2        struct{ Code, Message int }
	assignJobOff     struct{ JobID, Job int }
	abortJobOff      struct{ JobID, Reason int }
	narPushResumeOff struct{ JobID, StorePath, ReceivedBytes int }
	cacheStatusOff   struct{ QueryID, Cached int }
	cacheErrorOff    struct{ QueryID, Message int }
)

func init() {
	l := wire.TagField()
	authChallengeOff.Peers = l.Field(wire.VecAlign, wire.VecSize)

	l = wire.TagField()
	initAckOff.Version = l.Field(2, 2)
	initAckOff.Capabilities = l.Field(1, capabilitiesSize)
	initAckOff.AuthorizedPeers = l.Field(wire.VecAlign, wire.VecSize)
	initAckOff.FailedPeers = l.Field(wire.VecAlign, wire.VecSize)

	l = wire.TagField()
	rejectOff.Code = l.Field(2, 2)
	rejectOff.Reason = l.Field(wire.StringAlign, wire.StringSize)

	l = wire.TagField()
	errorOff2.Code = l.Field(2, 2)
	errorOff2.Message = l.Field(wire.StringAlign, wire.StringSize)

	l = wire.TagField()
	assignJobOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	assignJobOff.Job = l.Field(jobAlign, jobSize)

	l = wire.TagField()
	abortJobOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	abortJobOff.Reason = l.Field(wire.StringAlign, wire.StringSize)

	l = wire.TagField()
	narPushResumeOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	narPushResumeOff.StorePath = l.Field(wire.StringAlign, wire.StringSize)
	narPushResumeOff.ReceivedBytes = l.Field(8, 8)

	l = wire.TagField()
	cacheStatusOff.QueryID = l.Field(wire.StringAlign, wire.StringSize)
	cacheStatusOff.Cached = l.Field(wire.VecAlign, wire.VecSize)

	l = wire.TagField()
	cacheErrorOff.QueryID = l.Field(wire.StringAlign, wire.StringSize)
	cacheErrorOff.Message = l.Field(wire.StringAlign, wire.StringSize)
}

// jobAlign is Job's own measured alignment (8, not the tag's 1 byte) -
// see jobUpdateKindAlign's doc in client_message.go for why this is a
// distinct concern from the tag-width bug documented in TagField.
const jobAlign = 8

func decodeStringVec(d *wire.Decoder, pos int) []string {
	out := make([]string, 0, d.VecLen(pos))
	d.VecEach(pos, wire.StringSize, func(elemPos, index int) {
		out = append(out, d.String(elemPos))
	})
	return out
}

func decodeFailedPeerVec(d *wire.Decoder, pos int) []FailedPeer {
	out := make([]FailedPeer, 0, d.VecLen(pos))
	d.VecEach(pos, failedPeerSize, func(elemPos, index int) {
		out = append(out, decodeFailedPeer(d, elemPos))
	})
	return out
}

func decodeCachedPathVec(d *wire.Decoder, pos int) []CachedPath {
	out := make([]CachedPath, 0, d.VecLen(pos))
	d.VecEach(pos, cachedPathOff.Size, func(elemPos, index int) {
		out = append(out, decodeCachedPath(d, elemPos))
	})
	return out
}

// DecodeServerMessage decodes a ServerMessage from a full /proto WebSocket
// binary frame. Unrecognized variant tags (this worker doesn't implement
// the full 24-variant catalog - eval/fetch/federate/cache-only messages
// are out of scope, see testdata/gen-fixtures/README.md) decode to
// ServerMsgUnknown with UnknownTag set, rather than erroring - callers
// should log and ignore rather than treat it as a protocol violation,
// since new variants may be added upstream over time.
func DecodeServerMessage(buf []byte) (ServerMessage, error) {
	if len(buf) < serverMessageSize {
		return ServerMessage{}, fmt.Errorf("gradientproto: buffer too short for ServerMessage: %d bytes, want at least %d", len(buf), serverMessageSize)
	}
	pos := len(buf) - serverMessageSize
	d := wire.NewDecoder(buf)
	tag := uint32(d.Tag1(pos))

	switch tag {
	case serverTagAuthChallenge:
		return ServerMessage{
			Kind:  ServerMsgAuthChallenge,
			Peers: decodeStringVec(d, pos+authChallengeOff.Peers),
		}, nil

	case serverTagInitAck:
		return ServerMessage{
			Kind:            ServerMsgInitAck,
			Version:         d.U16(pos + initAckOff.Version),
			Capabilities:    decodeCapabilities(d, pos+initAckOff.Capabilities),
			AuthorizedPeers: decodeStringVec(d, pos+initAckOff.AuthorizedPeers),
			FailedPeers:     decodeFailedPeerVec(d, pos+initAckOff.FailedPeers),
		}, nil

	case serverTagReject:
		return ServerMessage{
			Kind:   ServerMsgReject,
			Code:   d.U16(pos + rejectOff.Code),
			Reason: d.String(pos + rejectOff.Reason),
		}, nil

	case serverTagError:
		return ServerMessage{
			Kind:    ServerMsgError,
			Code:    d.U16(pos + errorOff2.Code),
			Message: d.String(pos + errorOff2.Message),
		}, nil

	case serverTagDraining:
		return ServerMessage{Kind: ServerMsgDraining}, nil

	case serverTagAssignJob:
		job, err := DecodeJob(d, pos+assignJobOff.Job)
		if err != nil {
			return ServerMessage{}, fmt.Errorf("gradientproto: decoding AssignJob: %w", err)
		}
		return ServerMessage{
			Kind:  ServerMsgAssignJob,
			JobID: d.String(pos + assignJobOff.JobID),
			Job:   job,
		}, nil

	case serverTagAbortJob:
		return ServerMessage{
			Kind:   ServerMsgAbortJob,
			JobID:  d.String(pos + abortJobOff.JobID),
			Reason: d.String(pos + abortJobOff.Reason),
		}, nil

	case serverTagNarPushResume:
		return ServerMessage{
			Kind:          ServerMsgNarPushResume,
			JobID:         d.String(pos + narPushResumeOff.JobID),
			StorePath:     d.String(pos + narPushResumeOff.StorePath),
			ReceivedBytes: d.U64(pos + narPushResumeOff.ReceivedBytes),
		}, nil

	case serverTagCacheStatus:
		return ServerMessage{
			Kind:    ServerMsgCacheStatus,
			QueryID: d.String(pos + cacheStatusOff.QueryID),
			Cached:  decodeCachedPathVec(d, pos+cacheStatusOff.Cached),
		}, nil

	case serverTagCacheError:
		return ServerMessage{
			Kind:    ServerMsgCacheError,
			QueryID: d.String(pos + cacheErrorOff.QueryID),
			Message: d.String(pos + cacheErrorOff.Message),
		}, nil

	default:
		return ServerMessage{Kind: ServerMsgUnknown, UnknownTag: tag}, nil
	}
}
