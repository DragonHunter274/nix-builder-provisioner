#![allow(dead_code)]
use crate::types::*;
use rkyv::{Archive, Deserialize, Serialize};


/// Messages sent from the client (worker / federated peer) to the server.
#[derive(Archive, Serialize, Deserialize, Debug, Clone, PartialEq)]
#[rkyv(derive(Debug, PartialEq))]
pub enum ClientMessage {
    /// First message on every connection.  The peer declares its protocol
    /// version, capabilities, and persistent identity.  The server responds
    /// with [`super::server::ServerMessage::AuthChallenge`].
    InitConnection {
        version: u16,
        capabilities: GradientCapabilities,
        /// Persistent peer UUID, generated on first start and stored locally.
        id: String,
    },

    /// Response to [`super::server::ServerMessage::AuthChallenge`].
    /// Contains per-peer tokens for each peer the worker has credentials for.
    /// Pairs are `(peer_id, token)`.
    AuthResponse { tokens: Vec<(String, String)> },

    /// Request a new auth challenge from the server - sent when the worker
    /// has acquired a new peer token and wants to become authorized for that
    /// peer without reconnecting.
    ReauthRequest,

    /// Decline the connection after receiving
    /// [`super::server::ServerMessage::InitAck`].
    /// The peer closes the WebSocket immediately after sending this.
    Reject { code: u16, reason: String },

    /// Advertise build capacity.  Sent after a successful handshake by any
    /// peer with the `build` capability negotiated.
    WorkerCapabilities {
        /// Supported architectures as Nix system strings, e.g. `"x86_64-linux"`.
        architectures: Vec<String>,
        /// Nix system features (e.g. `"kvm"`, `"big-parallel"`), capacity-sorted.
        system_features: Vec<String>,
        /// Maximum number of concurrent builds this peer accepts.
        max_concurrent_builds: u32,
        /// Number of logical CPUs available to the worker.
        cpu_count: u32,
        /// Total physical RAM in MiB.
        ram_total_mb: u64,
        /// Relative single-core performance score (higher is faster).
        cpu_core_score: u32,
    },

    /// Live resource-utilisation heartbeat. Sent periodically while connected so
    /// the scheduler can score jobs against the worker's current load.
    WorkerMetrics {
        cpu_usage_pct: f32,
        ram_free_mb: u64,
        disk_speed_mbps: Option<f32>,
        network_speed_mbps: Option<f32>,
    },

    /// Request the full current job candidate list as a stream of
    /// [`super::server::ServerMessage::JobListChunk`] messages.  Sent once
    /// after the handshake to bootstrap the local candidate cache.
    RequestJobList,

    /// Stream pre-computed job scores to the server.  Sent incrementally as
    /// the worker checks `required_paths` against its local Nix store.
    /// `is_final: true` marks the last chunk for the current scoring pass.
    RequestJobChunk {
        scores: Vec<CandidateScore>,
        is_final: bool,
    },

    /// Accept or reject a [`super::server::ServerMessage::AssignJob`].
    AssignJobResponse {
        job_id: String,
        accepted: bool,
        /// Set when `accepted` is `false`.
        reason: Option<String>,
    },

    /// Incremental progress update for an in-flight job.
    /// The server maps these directly to `EvaluationStatus` / `BuildStatus`.
    JobUpdate {
        job_id: String,
        update: JobUpdateKind,
    },

    /// All tasks in a job completed successfully.
    /// Results were already sent via [`ClientMessage::JobUpdate`].
    /// Per-build resource metrics travel inline on each `JobUpdate::BuildOutput`.
    JobCompleted { job_id: String },

    /// A task in the job failed; remaining tasks are skipped.
    JobFailed {
        job_id: String,
        error: String,
        kind: BuildFailureKind,
        /// For `BuildFailureKind::InputsUnavailable`: the required input store
        /// paths the cache could not serve. Empty for every other kind.
        missing_paths: Vec<String>,
    },

    /// Worker is draining - it will finish in-flight jobs then disconnect.
    /// Server stops assigning new jobs to this peer.
    Draining,

    /// Build log lines from an in-flight task.  Fire-and-forget.
    LogChunk {
        job_id: String,
        task_index: u32,
        data: Vec<u8>,
    },

    /// Request specific store paths from the server (direct NAR mode).
    NarRequest { job_id: String, paths: Vec<String> },

    /// One chunk of a NAR being pushed from worker to server (direct mode).
    NarPush {
        job_id: String,
        store_path: String,
        /// zstd-compressed NAR data, ~4 MiB chunks.
        data: Vec<u8>,
        offset: u64,
        is_final: bool,
    },

    /// Worker has finished uploading a NAR (via NarPush or presigned S3) and
    /// reports metadata so the server can update `cached_path` / `derivation_output`.
    NarUploaded {
        job_id: String,
        store_path: String,
        /// SHA-256 hash of the compressed NAR file (`sha256:<hex>`).
        file_hash: String,
        /// Size in bytes of the compressed NAR file.
        file_size: u64,
        /// Size in bytes of the uncompressed NAR.
        nar_size: u64,
        /// Hash of the uncompressed NAR (`sha256:<nix32>` or SRI format).
        nar_hash: String,
        /// Store-path references in hash-name format (without `/nix/store/` prefix).
        /// Empty when the worker could not query local path info.
        references: Vec<String>,
        /// Full deriver `.drv` path that produced this output, if known.
        /// `None` when the worker could not query local path info or when the
        /// path has no deriver (e.g. sources, `.drv` files themselves).
        deriver: Option<String>,
        /// Content address of the path in narinfo form
        /// (`text:sha256:<b32>` / `fixed:[r:]sha256:<b32>`), when the path is
        /// content-addressed. `None` for input-addressed paths.
        ca: Option<String>,
    },

    /// Opens a push stream for `store_path`; sent before the first `NarPush`.
    /// The worker then waits for [`super::server::ServerMessage::NarPushResume`]
    /// to learn how many compressed bytes the server already holds, then seeks
    /// its regenerated zstd stream to that offset before sending chunks.
    NarStreamHeader {
        job_id: String,
        store_path: String,
        /// Known uncompressed nar_size if available, else `None` (informational).
        total_bytes: Option<u64>,
        /// zstd identity; a mismatch on resume forces a restart from offset 0.
        stream_token: String,
    },

    /// Pull resume: the worker already holds `received_bytes` compressed bytes
    /// of this path's `.nar.zst` on disk and asks the server to continue the
    /// download from that offset instead of re-sending from 0.
    NarRequestResume {
        job_id: String,
        store_path: String,
        received_bytes: u64,
        stream_token: String,
    },

    /// Request the eval-cache SQLite blob for `fingerprint`.  The server
    /// answers with [`super::server::ServerMessage::EvalCachePullResult`].
    EvalCachePull { job_id: String, fingerprint: String },

    /// Announce an eval-cache blob the worker wants to upload.  The server
    /// answers with [`super::server::ServerMessage::EvalCachePushGrant`]
    /// granting a presigned PUT, an inline stream, or `Skip`.
    EvalCachePush {
        job_id: String,
        fingerprint: String,
        size_bytes: u64,
    },

    /// One chunk of an eval-cache blob being pushed inline (local-FS fallback).
    EvalCacheChunk {
        job_id: String,
        data: Vec<u8>,
        offset: u64,
        is_final: bool,
    },

    /// Confirms a presigned eval-cache PUT completed so the server can record
    /// the blob for `fingerprint`.
    EvalCachePushDone {
        job_id: String,
        fingerprint: String,
        size_bytes: u64,
    },

    /// Pull-based capacity signal: worker is ready to accept one job of the
    /// given kind.
    ///
    /// Sent after the handshake for each available slot (once per free eval
    /// slot and once per free build slot).  Re-sent immediately after an
    /// [`super::server::ServerMessage::AssignJob`] is received if the worker
    /// still has spare capacity.  Re-sent every 10 s as a heartbeat in case
    /// the server restarted and lost the pending request.
    ///
    /// The server assigns the first matching pending job directly - no scoring
    /// round-trip needed.
    RequestJob { kind: JobKind },

    /// Request the full current job candidate list from the server.
    /// Sent once at startup (alongside [`ClientMessage::RequestJobList`]) so
    /// the server can send the worker its initial candidate set.  All
    /// subsequent candidate updates arrive as delta [`super::server::ServerMessage::JobOffer`]
    /// messages and do not require another `RequestAllCandidates`.
    RequestAllCandidates,

    /// Bulk query against the server cache.
    ///
    /// Server responds with [`super::server::ServerMessage::CacheStatus`].
    /// [`QueryMode`] controls what the server returns beyond the cached flag:
    /// - `Normal` - only paths already in the cache (no URLs).
    /// - `Pull`   - cached paths with presigned S3 GET URLs (or `url: None` for local).
    /// - `Push`   - all paths; uncached ones include presigned S3 PUT URLs (or `url: None`).
    CacheQuery {
        job_id: String,
        /// Unique per-query id the server echoes in its [`super::server::ServerMessage::CacheStatus`]
        /// / [`super::server::ServerMessage::CacheError`] reply, so concurrent or
        /// retried queries under one `job_id` never steal each other's answer.
        query_id: String,
        paths: Vec<String>,
        /// Defaults to [`QueryMode::Normal`] when deserialized from an older client.
        mode: QueryMode,
    },

    /// Surface an infrastructure-level message tied to the active job's
    /// evaluation. The server inserts a row into `evaluation_message` so
    /// operators see transport, prefetch, or NAR-import problems directly on
    /// the evaluation page without drilling into individual build logs.
    ///
    /// This is **not** meant for build compile failures or user-initiated
    /// aborts - those are already reported via `JobFailed` and deliberately
    /// stay out of the evaluation log.
    EvalMessage {
        job_id: String,
        level: EvalMessageLevel,
        /// Short origin tag, e.g. `"build-prefetch"` or `"nar-import"`.
        source: String,
        message: String,
    },

    /// Query which of the given `.drv` paths the server already has recorded in
    /// its derivation table for the org that owns `job_id`.
    ///
    /// Server responds with [`super::server::ServerMessage::KnownDerivations`].
    /// The worker uses the response to prune BFS subtrees: if a derivation is
    /// already fully recorded on the server, there is no need to traverse its
    /// `inputDrvs` again.
    QueryKnownDerivations {
        job_id: String,
        drv_paths: Vec<String>,
    },
}

impl ClientMessage {
    /// Static name of the variant. Used for log messages where dumping the
    /// full Debug-formatted message would be unsafe (e.g. `NarPush` carries
    /// up to 64 KiB of binary chunk data).
    pub fn variant_name(&self) -> &'static str {
        match self {
            ClientMessage::InitConnection { .. } => "InitConnection",
            ClientMessage::AuthResponse { .. } => "AuthResponse",
            ClientMessage::ReauthRequest => "ReauthRequest",
            ClientMessage::Reject { .. } => "Reject",
            ClientMessage::WorkerCapabilities { .. } => "WorkerCapabilities",
            ClientMessage::WorkerMetrics { .. } => "WorkerMetrics",
            ClientMessage::RequestJobList => "RequestJobList",
            ClientMessage::RequestJobChunk { .. } => "RequestJobChunk",
            ClientMessage::AssignJobResponse { .. } => "AssignJobResponse",
            ClientMessage::JobUpdate { .. } => "JobUpdate",
            ClientMessage::JobCompleted { .. } => "JobCompleted",
            ClientMessage::JobFailed { .. } => "JobFailed",
            ClientMessage::Draining => "Draining",
            ClientMessage::LogChunk { .. } => "LogChunk",
            ClientMessage::NarRequest { .. } => "NarRequest",
            ClientMessage::NarStreamHeader { .. } => "NarStreamHeader",
            ClientMessage::NarRequestResume { .. } => "NarRequestResume",
            ClientMessage::NarPush { .. } => "NarPush",
            ClientMessage::NarUploaded { .. } => "NarUploaded",
            ClientMessage::EvalCachePull { .. } => "EvalCachePull",
            ClientMessage::EvalCachePush { .. } => "EvalCachePush",
            ClientMessage::EvalCacheChunk { .. } => "EvalCacheChunk",
            ClientMessage::EvalCachePushDone { .. } => "EvalCachePushDone",
            ClientMessage::RequestJob { .. } => "RequestJob",
            ClientMessage::RequestAllCandidates => "RequestAllCandidates",
            ClientMessage::CacheQuery { .. } => "CacheQuery",
            ClientMessage::EvalMessage { .. } => "EvalMessage",
            ClientMessage::QueryKnownDerivations { .. } => "QueryKnownDerivations",
        }
    }
}


/// A peer that failed authentication during the challenge-response flow.
#[derive(Archive, Serialize, Deserialize, Debug, Clone, PartialEq)]
#[rkyv(derive(Debug, PartialEq))]
pub struct FailedPeer {
    pub peer_id: String,
    pub reason: String,
}

/// Messages sent from the server to the client (worker / federated peer).
#[derive(Archive, Serialize, Deserialize, Debug, Clone, PartialEq)]
#[rkyv(derive(Debug, PartialEq))]
pub enum ServerMessage {
    /// Challenge sent after `InitConnection`.  Lists the peer IDs that have
    /// registered this worker ID - the worker must respond with tokens for
    /// each peer it has credentials for.
    AuthChallenge { peers: Vec<String> },

    /// Successful handshake response.  Contains the negotiated capabilities
    /// and the set of peers this worker is now authorized for.
    InitAck {
        version: u16,
        capabilities: GradientCapabilities,
        /// Peer IDs whose tokens were accepted.
        authorized_peers: Vec<String>,
        /// Peers whose tokens were missing or invalid.
        failed_peers: Vec<FailedPeer>,
    },

    /// Sent after a mid-connection reauth completes (triggered by
    /// [`super::client::ClientMessage::ReauthRequest`] or by the server when
    /// a new peer registers this worker).
    AuthUpdate {
        authorized_peers: Vec<String>,
        failed_peers: Vec<FailedPeer>,
    },

    /// Server declines the connection.  Closes after sending.
    Reject { code: u16, reason: String },

    /// Protocol-level error.  The connection may be closed after this.
    Error { code: u16, message: String },

    /// Server is shutting down gracefully.  Workers should finish in-flight
    /// jobs, buffer results, and delay reconnection.
    Draining,

    /// Chunk of the full job candidate list, sent in response to
    /// [`super::client::ClientMessage::RequestJobList`].
    /// `is_final: true` marks the end.
    JobListChunk {
        candidates: Vec<JobCandidate>,
        is_final: bool,
    },

    /// Incremental push of new job candidates as they become available
    /// (e.g. evaluation discovers new derivations).
    /// Paginated at 1 000 entries per message.
    JobOffer { candidates: Vec<JobCandidate> },

    /// Remove candidates from the worker's local cache - they have been
    /// assigned to another worker or cancelled.
    RevokeJob { job_ids: Vec<String> },

    /// Assign a job to this worker.  Worker must respond with
    /// [`super::client::ClientMessage::AssignJobResponse`] before starting
    /// work.
    AssignJob { job_id: String, job: Job },

    /// Cancel an in-progress job.  Worker stops, cleans up, and responds
    /// with [`super::client::ClientMessage::JobFailed`].
    AbortJob { job_id: String, reason: String },

    /// Deliver a short-lived credential.  Sent before or alongside
    /// [`ServerMessage::AssignJob`] for tasks that need it.
    Credential { kind: CredentialKind, data: Vec<u8> },

    /// One chunk of a NAR being pushed from server to worker (direct mode).
    NarPush {
        job_id: String,
        store_path: String,
        /// zstd-compressed NAR data, ~4 MiB chunks.
        data: Vec<u8>,
        offset: u64,
        is_final: bool,
    },

    /// Sent in response to a [`super::client::ClientMessage::NarRequest`] when
    /// the server cannot serve the requested path at all (e.g. the
    /// `cached_path` row exists but the NAR bytes are not in `nar_storage`).
    /// No `NarPush` chunks will follow for this path. The worker must
    /// resolve any waiter for `(job_id, store_path)` with this `reason`
    /// instead of waiting for `is_final`.
    NarUnavailable {
        job_id: String,
        store_path: String,
        reason: String,
    },

    /// Sent during an in-flight NAR transfer when the server can no longer
    /// continue (e.g. the WebSocket write failed after some chunks, or the
    /// underlying storage stream errored). The worker must discard any
    /// partial buffer for `(job_id, store_path)` and resolve the waiter
    /// with this `reason`. No further `NarPush` chunks will arrive for
    /// this path on this transfer.
    NarAbort {
        job_id: String,
        store_path: String,
        reason: String,
    },

    /// Opens a pull stream for `store_path`; sent before the first `NarPush`
    /// so the worker can size and validate its `.partial`. The server always
    /// knows the stored object's `total_bytes`.
    NarStreamHeader {
        job_id: String,
        store_path: String,
        total_bytes: u64,
        stream_token: String,
    },

    /// Push resume ack, sent in response to a worker's
    /// [`super::client::ClientMessage::NarStreamHeader`]. `received_bytes` is
    /// how many compressed bytes the server already holds in the matching
    /// `.partial`; `0` means fresh / token mismatch / nothing on disk.
    NarPushResume {
        job_id: String,
        store_path: String,
        received_bytes: u64,
    },

    /// Result of a [`super::client::ClientMessage::EvalCachePull`].  The
    /// `outcome` carries a miss, a presigned GET URL, or an inline-stream
    /// header; inline blobs then arrive as [`ServerMessage::EvalCacheChunk`].
    EvalCachePullResult {
        job_id: String,
        outcome: EvalCachePullOutcome,
    },

    /// One chunk of an eval-cache blob being streamed inline to the worker
    /// (local-FS fallback for [`EvalCachePullOutcome::Inline`]).
    EvalCacheChunk {
        job_id: String,
        data: Vec<u8>,
        offset: u64,
        is_final: bool,
    },

    /// Response to a [`super::client::ClientMessage::EvalCachePush`] granting
    /// a presigned PUT, an inline upload, or `Skip` when the blob is known.
    EvalCachePushGrant {
        job_id: String,
        mode: EvalCachePushMode,
    },

    /// Ask a newly connected worker to send its full candidate score set.
    /// Sent once by the server during the initial handshake completion so it
    /// can populate its in-memory score table.  After startup all score
    /// updates arrive as delta [`super::client::ClientMessage::RequestJobChunk`]
    /// messages - `RequestAllScores` is not sent again.
    RequestAllScores,

    /// Response to [`super::client::ClientMessage::CacheQuery`].
    /// Paths in the local Gradient cache have `url: None`; paths found in upstream
    /// external Nix caches have `url: Some(absolute_nar_url)`.
    CacheStatus {
        /// Echoes the [`super::client::ClientMessage::CacheQuery`] `query_id`; it is
        /// the sole correlator, so the worker routes this reply to the exact query
        /// that sent it (the worker holds the owning `job_id` locally).
        query_id: String,
        cached: Vec<CachedPath>,
    },

    /// Response to [`super::client::ClientMessage::QueryKnownDerivations`].
    ///
    /// Contains the subset of the queried `.drv` paths that are already
    /// recorded in the server's derivation table for the owning org.
    /// The worker skips subtree traversal for these paths during BFS.
    KnownDerivations { job_id: String, known: Vec<String> },

    /// The server could not *determine* cache state for a
    /// [`super::client::ClientMessage::CacheQuery`] (a transient DB error or an
    /// over-budget handler). Distinct from a `CacheStatus` listing paths as
    /// uncached: the worker must treat this as a retryable transport failure,
    /// never as "inputs missing", so a server-side hiccup cannot poison a build.
    CacheError {
        /// Echoes the [`super::client::ClientMessage::CacheQuery`] `query_id`.
        query_id: String,
        message: String,
    },
}

impl ServerMessage {
    /// Static name of the variant. Used for log messages where dumping the
    /// full Debug-formatted message would be unsafe (e.g. `NarPush` carries
    /// megabytes of binary chunk data).
    pub fn variant_name(&self) -> &'static str {
        match self {
            ServerMessage::AuthChallenge { .. } => "AuthChallenge",
            ServerMessage::InitAck { .. } => "InitAck",
            ServerMessage::AuthUpdate { .. } => "AuthUpdate",
            ServerMessage::Reject { .. } => "Reject",
            ServerMessage::Error { .. } => "Error",
            ServerMessage::Draining => "Draining",
            ServerMessage::JobListChunk { .. } => "JobListChunk",
            ServerMessage::JobOffer { .. } => "JobOffer",
            ServerMessage::RevokeJob { .. } => "RevokeJob",
            ServerMessage::AssignJob { .. } => "AssignJob",
            ServerMessage::AbortJob { .. } => "AbortJob",
            ServerMessage::Credential { .. } => "Credential",
            ServerMessage::NarPush { .. } => "NarPush",
            ServerMessage::NarUnavailable { .. } => "NarUnavailable",
            ServerMessage::NarAbort { .. } => "NarAbort",
            ServerMessage::NarStreamHeader { .. } => "NarStreamHeader",
            ServerMessage::NarPushResume { .. } => "NarPushResume",
            ServerMessage::EvalCachePullResult { .. } => "EvalCachePullResult",
            ServerMessage::EvalCacheChunk { .. } => "EvalCacheChunk",
            ServerMessage::EvalCachePushGrant { .. } => "EvalCachePushGrant",
            ServerMessage::RequestAllScores => "RequestAllScores",
            ServerMessage::CacheStatus { .. } => "CacheStatus",
            ServerMessage::KnownDerivations { .. } => "KnownDerivations",
            ServerMessage::CacheError { .. } => "CacheError",
        }
    }
}
