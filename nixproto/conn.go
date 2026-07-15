package nixproto

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
)

// Conn represents a Nix daemon protocol connection
type Conn struct {
	ctx           context.Context
	r             *bufio.Reader
	w             *bufio.Writer
	Version       uint64 // Negotiated protocol version
	ClientTrusted bool   // Whether we trust the client
}

// NewConn creates a new Nix protocol connection wrapper
func NewConn(r io.Reader, w io.Writer) *Conn {
	return NewConnWithContext(context.Background(), r, w)
}

// NewConnWithContext creates a new Nix protocol connection with context for cancellation
func NewConnWithContext(ctx context.Context, r io.Reader, w io.Writer) *Conn {
	return &Conn{
		ctx: ctx,
		r:   bufio.NewReader(r),
		w:   bufio.NewWriter(w),
	}
}

// Context returns the connection's context
func (c *Conn) Context() context.Context {
	return c.ctx
}

// Reader returns the underlying reader
func (c *Conn) Reader() io.Reader {
	return c.r
}

// Writer returns the underlying writer
func (c *Conn) Writer() io.Writer {
	return c.w
}

// Flush flushes the buffered writer
func (c *Conn) Flush() error {
	return c.w.Flush()
}

// Handshake performs the Nix daemon protocol handshake as a server
func (c *Conn) Handshake() error {
	// 1. Read client magic
	clientMagic, err := ReadUint64(c.r)
	if err != nil {
		return fmt.Errorf("reading client magic: %w", err)
	}
	// log.Printf("DEBUG: Read client magic: %#x", clientMagic)
	if clientMagic != ClientMagic {
		return fmt.Errorf("invalid client magic: got %x, expected %x", clientMagic, ClientMagic)
	}

	// 2. Write server magic
	// log.Printf("DEBUG: Writing server magic: %#x", ServerMagic)
	if err := WriteUint64(c.w, ServerMagic); err != nil {
		return fmt.Errorf("writing server magic: %w", err)
	}

	// 3. Write our protocol version
	// log.Printf("DEBUG: Writing protocol version: %#x (%d.%d)", ProtocolVersion, ProtocolVersionMajor, ProtocolVersionMinor)
	if err := WriteUint64(c.w, ProtocolVersion); err != nil {
		return fmt.Errorf("writing protocol version: %w", err)
	}

	// Flush to ensure client receives our response before sending theirs
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("flushing after version exchange: %w", err)
	}
	// log.Printf("DEBUG: Flushed magic and version")

	// 4. Read client's protocol version
	clientVersion, err := ReadUint64(c.r)
	if err != nil {
		return fmt.Errorf("reading client version: %w", err)
	}

	// 5. Read client's features (NEW)
	_, err = ReadStrings(c.r)
	if err != nil {
		return fmt.Errorf("reading client features: %w", err)
	}
	// log.Printf("DEBUG: Read client features: %v", clientFeatures)

	// Use the minimum of client and server versions
	c.Version = clientVersion
	if ProtocolVersion < clientVersion {
		c.Version = ProtocolVersion
	}

	log.Printf("Nix protocol handshake: client version %d.%d, using %d.%d",
		(clientVersion>>8)&0xff, clientVersion&0xff,
		(c.Version>>8)&0xff, c.Version&0xff)

	// 6. Write server's features (NEW)
	// For now, an empty feature set.
	if err := WriteStrings(c.w, []string{}); err != nil {
		return fmt.Errorf("writing server features: %w", err)
	}
	// log.Printf("DEBUG: Wrote server features (empty)")

	// Flush so client receives features before sending affinity/reserveSpace
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("flushing after features: %w", err)
	}

	// 7. For protocol >= 1.14, exchange additional info (affinity, reserve)
	minor := c.Version & 0xff
	if minor >= 14 {
		// Read CPU affinity (obsolete, ignore). This field is CONDITIONAL:
		// the client sends a single uint64 flag, and only if it is non-zero
		// does a second uint64 (the actual affinity mask) follow - matching
		// real Nix's `if (readInt(from)) { readInt(from); }`. Always reading
		// two values here desyncs the entire rest of the session for any
		// client that actually sets CPU affinity.
		affinity, err := ReadUint64(c.r)
		if err != nil {
			return fmt.Errorf("reading obsolete affinity: %w", err)
		}
		if affinity != 0 {
			if _, err := ReadUint64(c.r); err != nil {
				return fmt.Errorf("reading obsolete affinity mask: %w", err)
			}
		}

		// Read reserve space (obsolete, ignore)
		// Both the Haskell client and the C++ client send reserveSpace as a uint64_t (8 bytes).
		// This field was previously incorrectly assumed to be a single byte from Haskell.
		if _, err := ReadUint64(c.r); err != nil {
			return fmt.Errorf("reading obsolete reserve (uint64): %w", err)
		}
		// Note: Server does NOT send affinity/reserveSpace back - it proceeds directly to ClientHandshakeInfo
	}

	// Final flush to ensure all handshake data is sent after the main handshake
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("flushing after handshake: %w", err)
	}

	return nil
}

// PostHandshake performs the second part of the Nix daemon protocol handshake as a server,
// sending the ClientHandshakeInfo.
func (c *Conn) PostHandshake() error {
	minor := c.Version & 0xff

	// Write ClientHandshakeInfo
	// Based on WorkerProto::Serialise<WorkerProto::ClientHandshakeInfo>::write in worker-protocol.cc

	// For protocol >= 1.33: Write daemon version string (plain string, NOT optional)
	if minor >= 33 {
		versionStr := "nix-builder-proxy (Nix) 2.24.0"
		// log.Printf("DEBUG: Writing daemon version string: %q (len=%d)", versionStr, len(versionStr))
		if err := WriteString(c.w, versionStr); err != nil {
			return fmt.Errorf("writing daemon version: %w", err)
		}
		// log.Printf("DEBUG: Wrote daemon version string (8 + %d + %d padding bytes)", len(versionStr), (8-len(versionStr)%8)%8)
	}

	// For protocol >= 1.35: Write trust status as uint64
	// 1 = Trusted, 0 = NotTrusted
	if minor >= 35 {
		// log.Printf("DEBUG: Writing trust status: 1 (Trusted)")
		if err := WriteUint64(c.w, 1); err != nil { // 1 = Trusted
			return fmt.Errorf("writing trust status: %w", err)
		}
		// log.Printf("DEBUG: Wrote trust status (Trusted)")
	}

	c.ClientTrusted = true

	// Send StderrLast to signal handshake is complete
	// This is required - the client waits for this before sending operations
	if err := WriteUint64(c.w, StderrLast); err != nil {
		return fmt.Errorf("writing StderrLast after handshake: %w", err)
	}
	// log.Printf("DEBUG: Wrote StderrLast to complete handshake")

	// Final flush to ensure all handshake data is sent before we start waiting for operations
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("flushing after post-handshake: %w", err)
	}

	return nil
}

// ReadOp reads the next operation code from the connection
func (c *Conn) ReadOp() (WorkerOp, error) {
	log.Printf("Waiting to read next operation...")
	op, err := ReadUint64(c.r)
	if err != nil {
		return 0, err
	}
	return WorkerOp(op), nil
}

// WriteOp writes an operation code
func (c *Conn) WriteOp(op WorkerOp) error {
	return WriteUint64(c.w, uint64(op))
}

// StartWork signals that we're starting to process an operation
// (switches to stderr streaming mode)
func (c *Conn) StartWork() error {
	// Nothing to write here - we just track state internally
	return nil
}

// StopWork signals that we're done processing an operation
func (c *Conn) StopWork() error {
	if err := WriteUint64(c.w, StderrLast); err != nil {
		return err
	}
	return c.w.Flush()
}

// StopWorkWithError signals an error during operation processing
func (c *Conn) StopWorkWithError(errMsg string, status int) error {
	if err := WriteUint64(c.w, StderrError); err != nil {
		return err
	}

	minor := c.Version & 0xff
	if minor >= 26 {
		// New error format: type, level, name, msg, havePos, traces
		// For simplicity, we'll write a basic error
		if err := WriteString(c.w, "Error"); err != nil { // type
			return err
		}
		if err := WriteUint64(c.w, 0); err != nil { // level (0 = error)
			return err
		}
		if err := WriteString(c.w, ""); err != nil { // name
			return err
		}
		if err := WriteString(c.w, errMsg); err != nil { // msg
			return err
		}
		if err := WriteUint64(c.w, 0); err != nil { // havePos
			return err
		}
		if err := WriteUint64(c.w, 0); err != nil { // traces count
			return err
		}
	} else {
		// Old error format: just string and status
		if err := WriteString(c.w, errMsg); err != nil {
			return err
		}
		if err := WriteUint64(c.w, uint64(status)); err != nil {
			return err
		}
	}

	return c.w.Flush()
}

// WriteStderrNext writes a log message during operation processing
func (c *Conn) WriteStderrNext(msg string) error {
	if err := WriteUint64(c.w, StderrNext); err != nil {
		return err
	}
	return WriteString(c.w, msg+"\n")
}

// ProxyStderr proxies stderr messages from a backend connection to the client
func (c *Conn) ProxyStderr(backendR io.Reader) error {
	for {
		marker, err := ReadUint64(backendR)
		if err != nil {
			return fmt.Errorf("reading stderr marker: %w", err)
		}

		// Write marker to client
		if err := WriteUint64(c.w, marker); err != nil {
			return err
		}

		switch marker {
		case StderrLast:
			// End of stderr stream
			return nil

		case StderrNext:
			// Log message
			msg, err := ReadString(backendR)
			if err != nil {
				return err
			}
			if err := WriteString(c.w, msg); err != nil {
				return err
			}

		case StderrError:
			// Error - proxy the error details based on protocol version
			minor := c.Version & 0xff
			if minor >= 26 {
				// New error format
				errType, _ := ReadString(backendR)
				WriteString(c.w, errType)
				level, _ := ReadUint64(backendR)
				WriteUint64(c.w, level)
				name, _ := ReadString(backendR)
				WriteString(c.w, name)
				msg, _ := ReadString(backendR)
				WriteString(c.w, msg)
				havePos, _ := ReadUint64(backendR)
				WriteUint64(c.w, havePos)
				tracesCount, _ := ReadUint64(backendR)
				WriteUint64(c.w, tracesCount)
				// Note: if tracesCount > 0, would need to proxy traces too
			} else {
				msg, _ := ReadString(backendR)
				WriteString(c.w, msg)
				status, _ := ReadUint64(backendR)
				WriteUint64(c.w, status)
			}
			return fmt.Errorf("backend returned error")

		case StderrWrite:
			// Data for sink - proxy the data
			data, err := ReadBytes(backendR)
			if err != nil {
				return err
			}
			if err := WriteBytes(c.w, data); err != nil {
				return err
			}

		case StderrRead:
			// Backend needs data from source
			// Read length it wants
			length, err := ReadUint64(backendR)
			if err != nil {
				return err
			}
			if err := WriteUint64(c.w, length); err != nil {
				return err
			}
			// Now we need to read from client and send to backend
			// This requires bidirectional proxy which is complex
			// For now, return error
			return fmt.Errorf("StderrRead not yet supported in proxy")

		case StderrStartActivity, StderrStopActivity, StderrResult:
			// Activity tracking - proxy the activity data
			// These have complex formats, for now just proxy raw
			actID, _ := ReadUint64(backendR)
			WriteUint64(c.w, actID)
			if marker == StderrStartActivity {
				level, _ := ReadUint64(backendR)
				WriteUint64(c.w, level)
				actType, _ := ReadUint64(backendR)
				WriteUint64(c.w, actType)
				text, _ := ReadString(backendR)
				WriteString(c.w, text)
				// Fields
				fieldsCount, _ := ReadUint64(backendR)
				WriteUint64(c.w, fieldsCount)
				for i := uint64(0); i < fieldsCount; i++ {
					fieldType, _ := ReadUint64(backendR)
					WriteUint64(c.w, fieldType)
					if fieldType == 0 { // int
						v, _ := ReadUint64(backendR)
						WriteUint64(c.w, v)
					} else { // string
						s, _ := ReadString(backendR)
						WriteString(c.w, s)
					}
				}
				parent, _ := ReadUint64(backendR)
				WriteUint64(c.w, parent)
			} else if marker == StderrResult {
				resType, _ := ReadUint64(backendR)
				WriteUint64(c.w, resType)
				fieldsCount, _ := ReadUint64(backendR)
				WriteUint64(c.w, fieldsCount)
				for i := uint64(0); i < fieldsCount; i++ {
					fieldType, _ := ReadUint64(backendR)
					WriteUint64(c.w, fieldType)
					if fieldType == 0 {
						v, _ := ReadUint64(backendR)
						WriteUint64(c.w, v)
					} else {
						s, _ := ReadString(backendR)
						WriteString(c.w, s)
					}
				}
			}

		default:
			return fmt.Errorf("unknown stderr marker: %x", marker)
		}
	}
}

// WriteStderrLog writes a log message (StderrNext)
func (c *Conn) WriteStderrLog(msg string) error {
	if err := WriteUint64(c.w, StderrNext); err != nil {
		return err
	}
	// WriteString writes the length-prefixed string.
	// Nix daemon logs usually don't have a trailing newline in the message itself
	// but are printed with one. If we forward, we just send what we got.
	return WriteString(c.w, msg)
}

// ActivityField represents a field in StartActivity or Result
type ActivityField struct {
	Type   uint64
	IntVal uint64
	StrVal string
}

// WriteStderrStartActivity writes a StderrStartActivity message
func (c *Conn) WriteStderrStartActivity(act, level, type_ uint64, text string, fields []ActivityField, parent uint64) error {
	if err := WriteUint64(c.w, StderrStartActivity); err != nil {
		return err
	}
	if err := WriteUint64(c.w, act); err != nil {
		return err
	}
	if err := WriteUint64(c.w, level); err != nil {
		return err
	}
	if err := WriteUint64(c.w, type_); err != nil {
		return err
	}
	if err := WriteString(c.w, text); err != nil {
		return err
	}
	if err := WriteUint64(c.w, uint64(len(fields))); err != nil {
		return err
	}
	for _, f := range fields {
		if err := WriteUint64(c.w, f.Type); err != nil {
			return err
		}
		if f.Type == 0 {
			if err := WriteUint64(c.w, f.IntVal); err != nil {
				return err
			}
		} else {
			if err := WriteString(c.w, f.StrVal); err != nil {
				return err
			}
		}
	}
	return WriteUint64(c.w, parent)
}

// WriteStderrStopActivity writes a StderrStopActivity message
func (c *Conn) WriteStderrStopActivity(act uint64) error {
	if err := WriteUint64(c.w, StderrStopActivity); err != nil {
		return err
	}
	return WriteUint64(c.w, act)
}

// WriteStderrResult writes a StderrResult message
func (c *Conn) WriteStderrResult(act, type_ uint64, fields []ActivityField) error {
	if err := WriteUint64(c.w, StderrResult); err != nil {
		return err
	}
	if err := WriteUint64(c.w, act); err != nil {
		return err
	}
	if err := WriteUint64(c.w, type_); err != nil {
		return err
	}
	if err := WriteUint64(c.w, uint64(len(fields))); err != nil {
		return err
	}
	for _, f := range fields {
		if err := WriteUint64(c.w, f.Type); err != nil {
			return err
		}
		if f.Type == 0 {
			if err := WriteUint64(c.w, f.IntVal); err != nil {
				return err
			}
		} else {
			if err := WriteString(c.w, f.StrVal); err != nil {
				return err
			}
		}
	}
	return nil
}
