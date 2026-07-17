package gradientproto

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

	"github.com/klauspost/compress/zstd"
)

// narPushChunkSize matches Gradient's own session/frame.rs
// NAR_PUSH_CHUNK_SIZE, so our chunk boundaries look like any other
// worker's to the server (no functional requirement to match exactly -
// the protocol doesn't encode a chunk size anywhere - but there's no
// reason to differ either).
const narPushChunkSize = 4 * 1024 * 1024

// uploadResult carries what EncodeNarUploaded needs about the compressed
// upload, computed while streaming rather than after the fact (the
// compressed bytes are never fully buffered).
type uploadResult struct {
	fileHash string // "sha256:<hex>"
	fileSize uint64
}

// compressNarToPipe starts `nix-store --dump <path>` on exec (raw NAR
// bytes to stdout) and pipes it through a zstd encoder, writing the
// compressed stream to pw. Runs until the dump completes or ctx is
// cancelled; always closes pw (with an error, if one occurred, so the
// reader side observes it via io.Pipe's error propagation).
func compressNarToPipe(ctx context.Context, exec Execer, path string, pw *io.PipeWriter) {
	stdout, wait, err := exec.StreamOutput(ctx, fmt.Sprintf("nix-store --dump %s", shellQuote(path)))
	if err != nil {
		pw.CloseWithError(fmt.Errorf("gradientproto: starting nix-store --dump for %s: %w", path, err))
		return
	}

	enc, err := zstd.NewWriter(pw)
	if err != nil {
		pw.CloseWithError(fmt.Errorf("gradientproto: creating zstd encoder: %w", err))
		return
	}

	_, copyErr := io.Copy(enc, stdout)
	closeErr := enc.Close()
	waitErr := wait()

	switch {
	case copyErr != nil:
		pw.CloseWithError(fmt.Errorf("gradientproto: compressing NAR for %s: %w", path, copyErr))
	case closeErr != nil:
		pw.CloseWithError(fmt.Errorf("gradientproto: finalizing zstd stream for %s: %w", path, closeErr))
	case waitErr != nil:
		pw.CloseWithError(fmt.Errorf("gradientproto: nix-store --dump for %s failed: %w", path, waitErr))
	default:
		pw.Close()
	}
}

// pushOverWebSocket reads the zstd-compressed NAR from r and sends it to
// the server as a sequence of ClientMessage::NarPush chunks (no resume
// support in v1 - always starts at offset 0; see this package's plan doc
// for why NarStreamHeader/NarPushResume negotiation is deferred). Returns
// the compressed stream's total size and sha256 hash, computed from the
// same bytes actually sent.
func pushOverWebSocket(ctx context.Context, conn *Conn, r io.Reader, jobID, storePath string) (uploadResult, error) {
	h := sha256.New()
	tee := io.TeeReader(r, h)

	// is_final can only be known once a *following* read has confirmed
	// there's nothing left - a chunk landing exactly on narPushChunkSize
	// looks identical (n == len(buf), err == nil) whether or not it's the
	// last chunk. So each iteration holds the previous chunk as `pending`
	// and flushes it non-final only once a new chunk has actually arrived
	// to take its place; the loop's exit flushes whatever's left
	// (possibly nothing, for a genuinely empty stream) as the final chunk.
	var offset uint64
	var pending []byte
	flush := func(isFinal bool) error {
		if err := conn.SendClientMessage(ctx, EncodeNarPush(jobID, storePath, pending, offset, isFinal)); err != nil {
			return fmt.Errorf("gradientproto: sending NarPush chunk at offset %d: %w", offset, err)
		}
		offset += uint64(len(pending))
		return nil
	}

	buf := make([]byte, narPushChunkSize)
	for {
		n, readErr := io.ReadFull(tee, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return uploadResult{}, fmt.Errorf("gradientproto: reading compressed NAR for %s: %w", storePath, readErr)
		}
		if n > 0 {
			if pending != nil {
				if err := flush(false); err != nil {
					return uploadResult{}, err
				}
			}
			pending = append([]byte(nil), buf[:n]...)
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	if err := flush(true); err != nil {
		return uploadResult{}, err
	}

	return uploadResult{
		fileHash: "sha256:" + hex.EncodeToString(h.Sum(nil)),
		fileSize: offset,
	}, nil
}

// putPresigned uploads r's contents to a presigned S3 PUT URL via
// httpClient, returning the same summary pushOverWebSocket would. Used
// when CacheQuery{mode: Push} grants a presigned URL instead of routing
// through the WebSocket channel - Gradient's preferred fast path (skips
// proxying potentially large NAR bytes through the /proto connection).
func putPresigned(ctx context.Context, httpClient *http.Client, url string, r io.Reader, size int64) (uploadResult, error) {
	h := sha256.New()
	counting := &countingReader{r: io.TeeReader(r, h)}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, counting)
	if err != nil {
		return uploadResult{}, fmt.Errorf("gradientproto: building presigned PUT request: %w", err)
	}
	if size >= 0 {
		req.ContentLength = size
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return uploadResult{}, fmt.Errorf("gradientproto: presigned PUT failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return uploadResult{}, fmt.Errorf("gradientproto: presigned PUT returned status %d", resp.StatusCode)
	}

	return uploadResult{
		fileHash: "sha256:" + hex.EncodeToString(h.Sum(nil)),
		fileSize: uint64(counting.n),
	}, nil
}

// countingReader wraps an io.Reader and tracks total bytes read - used so
// putPresigned knows the exact byte count actually sent, independent of
// whatever ContentLength was declared upfront (which is unknown for a
// streamed zstd-compressed NAR - -1/unset lets net/http chunk the request
// instead of requiring a precomputed length).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// newQueryID returns a fresh identifier for correlating a CacheQuery with
// its CacheStatus/CacheError response - see Client.QueryCache's doc on why
// this must be unique per outstanding call. The protocol only requires
// this to be a string the server echoes back verbatim (see
// ClientMessage::CacheQuery's doc: "Unique per-query id") - it doesn't
// need to be a real RFC 4122 UUID, so a plain random hex string avoids an
// extra dependency for this alone.
func newQueryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on Linux only fails if the kernel CSPRNG itself
		// is broken - a condition this process can't meaningfully recover
		// from, and one every other SSH-key-generation path in this
		// codebase (main.go's generateOrLoadKeyPair, etc.) also doesn't
		// handle specially.
		panic(fmt.Sprintf("gradientproto: crypto/rand.Read failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// uploadOutput runs the full per-output upload flow for one build
// output's store path: queries the server via CacheQuery{mode: Push} to
// learn whether the path is already cached (nothing to do) or how to
// upload it (a presigned URL, or the WebSocket channel), performs the
// compress+upload if needed, and returns the pathInfo (for
// BuildOutput.hash/nar_size) plus the upload's file_hash/file_size (for
// NarUploaded). exec must be able to see path (the builder's SSH
// connection, right after the build that produced it - see executor.go).
func uploadOutput(ctx context.Context, client *Client, conn *Conn, exec Execer, httpClient *http.Client, jobID, path string) (*pathInfo, error) {
	info, err := queryPathInfo(ctx, exec, path)
	if err != nil {
		return nil, err
	}

	queryID := newQueryID()
	resp, err := client.QueryCache(ctx, conn, jobID, queryID, []string{path}, QueryModePush)
	if err != nil {
		return nil, fmt.Errorf("gradientproto: querying cache for %s: %w", path, err)
	}
	switch resp.Kind {
	case ServerMsgCacheStatus:
		// proceed below
	case ServerMsgCacheError:
		return nil, fmt.Errorf("gradientproto: server reported a cache-query error for %s: %s", path, resp.Message)
	default:
		return nil, fmt.Errorf("gradientproto: unexpected response to CacheQuery for %s: kind %v", path, resp.Kind)
	}
	if len(resp.Cached) != 1 {
		return nil, fmt.Errorf("gradientproto: CacheStatus for %s returned %d entries, want 1", path, len(resp.Cached))
	}
	status := resp.Cached[0]

	if status.Cached {
		// Already present server-side; nothing to upload. The caller still
		// needs pathInfo for BuildOutput's hash/nar_size fields, but no
		// NarUploaded is sent (there's nothing new to register).
		return info, nil
	}

	pr, pw := io.Pipe()
	go compressNarToPipe(ctx, exec, path, pw)

	var result uploadResult
	if status.URL != nil {
		result, err = putPresigned(ctx, httpClient, *status.URL, pr, -1)
	} else {
		result, err = pushOverWebSocket(ctx, conn, pr, jobID, path)
	}
	pr.Close()
	if err != nil {
		return nil, err
	}

	deriver := info.Deriver
	if err := conn.SendClientMessage(ctx, EncodeNarUploaded(
		jobID, path, result.fileHash, result.fileSize, info.NarSize, info.NarHash,
		info.References, deriver, info.CA,
	)); err != nil {
		return nil, fmt.Errorf("gradientproto: sending NarUploaded for %s: %w", path, err)
	}

	return info, nil
}
