// Package gradientproto implements the client (worker) side of Gradient's
// native /proto WebSocket protocol on top of the rkyv wire codec in
// gradientproto/wire. It covers the "build" capability subset only - see
// the plan this was built from (or CLAUDE.md) for what's deferred.
package gradientproto

import "nix-builder-provisioner/gradientproto/wire"

// GradientCapabilities mirrors gradient_types::proto::GradientCapabilities:
// feature flags exchanged during the handshake. All fields default to
// false. Field order matters - it determines wire layout.
type GradientCapabilities struct {
	Core     bool
	Federate bool
	Fetch    bool
	Eval     bool
	Build    bool
	Cache    bool
}

const capabilitiesSize = 6 // 6 bool fields, no padding (align 1)

func prepareCapabilities(a *wire.Arena, c GradientCapabilities) wire.FieldWriter {
	return wire.PrepareStruct(
		wire.Field{Offset: 0, Write: wire.PrepareBool(a, c.Core)},
		wire.Field{Offset: 1, Write: wire.PrepareBool(a, c.Federate)},
		wire.Field{Offset: 2, Write: wire.PrepareBool(a, c.Fetch)},
		wire.Field{Offset: 3, Write: wire.PrepareBool(a, c.Eval)},
		wire.Field{Offset: 4, Write: wire.PrepareBool(a, c.Build)},
		wire.Field{Offset: 5, Write: wire.PrepareBool(a, c.Cache)},
	)
}

func decodeCapabilities(d *wire.Decoder, pos int) GradientCapabilities {
	return GradientCapabilities{
		Core:     d.Bool(pos + 0),
		Federate: d.Bool(pos + 1),
		Fetch:    d.Bool(pos + 2),
		Eval:     d.Bool(pos + 3),
		Build:    d.Bool(pos + 4),
		Cache:    d.Bool(pos + 5),
	}
}
