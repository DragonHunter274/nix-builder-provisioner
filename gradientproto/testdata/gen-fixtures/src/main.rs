use rkyv_fixtures::messages::{ClientMessage, FailedPeer, ServerMessage};
use rkyv_fixtures::types::*;
use rkyv::rancor::Error as RkyvError;
use std::fs;
use std::path::Path;

fn dump(dir: &Path, name: &str, bytes: &[u8]) {
    let path = dir.join(format!("{name}.bin"));
    fs::write(&path, bytes).expect("write fixture");
    println!("{name}: {} bytes -> {}", bytes.len(), path.display());
}

fn ser<T>(dir: &Path, name: &str, value: &T)
where
    T: for<'a> rkyv::Serialize<
        rkyv::api::high::HighSerializer<
            rkyv::util::AlignedVec,
            rkyv::ser::allocator::ArenaHandle<'a>,
            RkyvError,
        >,
    >,
{
    let bytes = rkyv::to_bytes::<RkyvError>(value).expect("serialize");
    dump(dir, name, &bytes);
}

fn main() {
    let out_dir = Path::new("fixtures");
    fs::create_dir_all(out_dir).expect("mkdir fixtures");

    // ── GradientCapabilities: simplest struct, all-bool fields ─────────────
    ser(
        out_dir,
        "GradientCapabilities_default",
        &GradientCapabilities::default(),
    );
    ser(
        out_dir,
        "GradientCapabilities_build_only",
        &GradientCapabilities {
            core: false,
            federate: false,
            fetch: false,
            eval: false,
            build: true,
            cache: false,
        },
    );
    ser(
        out_dir,
        "GradientCapabilities_all_true",
        &GradientCapabilities {
            core: true,
            federate: true,
            fetch: true,
            eval: true,
            build: true,
            cache: true,
        },
    );

    // ── DerivationOutput / BuildTask / BuildJob / Job ───────────────────────
    let empty_build_task = BuildTask {
        build_id: "".into(),
        drv_path: "".into(),
        external_cached: false,
        is_fixed_output: false,
        outputs: vec![],
        timeout_secs: None,
        max_silent_secs: None,
    };
    ser(out_dir, "BuildTask_empty_strings_none", &empty_build_task);

    let build_task_long = BuildTask {
        build_id: "b-0000000000000000000000000000000000000001".into(),
        drv_path: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-hello-2.12.drv".into(),
        external_cached: true,
        is_fixed_output: true,
        outputs: vec![
            DerivationOutput {
                name: "out".into(),
                path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            },
            DerivationOutput {
                name: "dev".into(),
                path: "/nix/store/cccccccccccccccccccccccccccccccc-hello-2.12-dev".into(),
            },
        ],
        timeout_secs: Some(14400),
        max_silent_secs: Some(3600),
    };
    ser(out_dir, "BuildTask_populated", &build_task_long);

    let build_job_empty = BuildJob { builds: vec![] };
    ser(out_dir, "BuildJob_empty", &build_job_empty);

    let build_job_one = BuildJob {
        builds: vec![empty_build_task.clone()],
    };
    ser(out_dir, "BuildJob_one", &build_job_one);

    let build_job_three = BuildJob {
        builds: vec![
            empty_build_task.clone(),
            build_task_long.clone(),
            empty_build_task.clone(),
        ],
    };
    ser(out_dir, "BuildJob_three", &build_job_three);

    ser(out_dir, "Job_Build_empty", &Job::Build(build_job_empty));
    let job_build_three = Job::Build(build_job_three.clone());
    ser(out_dir, "Job_Build_three", &job_build_three);
    ser(
        out_dir,
        "Job_Flake",
        &Job::Flake(FlakeJob {
            tasks: vec![FlakeTask::FetchFlake, FlakeTask::EvaluateDerivations],
            source: FlakeSource::Repository {
                url: "https://github.com/example/repo".into(),
                commit: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef".into(),
            },
            wildcards: vec!["packages.*".into()],
            timeout_secs: None,
            input_overrides: vec![],
            input_update: None,
        }),
    );

    // ── BuildOutput / BuildMetrics / JobUpdateKind ──────────────────────────
    let build_output = BuildOutput {
        name: "out".into(),
        store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
        hash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
            .into(),
        nar_size: Some(123456),
        nar_hash: Some("sha256:abc".into()),
        products: vec![],
    };
    let build_metrics = BuildMetrics {
        peak_ram_mb: Some(512),
        cpu_time_ms: Some(9000),
        avg_cpu_pct: Some(87.5),
        disk_read_bytes: Some(1024),
        disk_write_bytes: Some(2048),
        oom_killed: false,
        build_time_ms: Some(12345),
        peak_network_mbps: None,
    };
    ser(out_dir, "BuildMetrics_default", &BuildMetrics::default());
    ser(out_dir, "BuildMetrics_populated", &build_metrics);

    ser(
        out_dir,
        "JobUpdateKind_Building",
        &JobUpdateKind::Building {
            build_id: "b-1".into(),
        },
    );
    ser(
        out_dir,
        "JobUpdateKind_BuildOutput_no_metrics",
        &JobUpdateKind::BuildOutput {
            build_id: "b-1".into(),
            outputs: vec![build_output.clone()],
            metrics: None,
            substituted: false,
        },
    );
    ser(
        out_dir,
        "JobUpdateKind_BuildOutput_with_metrics",
        &JobUpdateKind::BuildOutput {
            build_id: "b-1".into(),
            outputs: vec![build_output.clone(), build_output.clone()],
            metrics: Some(build_metrics.clone()),
            substituted: true,
        },
    );

    // ── Enums with only unit / simple variants ──────────────────────────────
    ser(out_dir, "JobKind_Flake", &JobKind::Flake);
    ser(out_dir, "JobKind_Build", &JobKind::Build);
    ser(out_dir, "CredentialKind_SshKey", &CredentialKind::SshKey);
    ser(
        out_dir,
        "BuildFailureKind_Transient",
        &BuildFailureKind::Transient,
    );
    ser(
        out_dir,
        "BuildFailureKind_Permanent",
        &BuildFailureKind::Permanent,
    );
    ser(
        out_dir,
        "BuildFailureKind_Timeout",
        &BuildFailureKind::Timeout,
    );
    ser(
        out_dir,
        "BuildFailureKind_SubstituteUnavailable",
        &BuildFailureKind::SubstituteUnavailable,
    );
    ser(
        out_dir,
        "BuildFailureKind_InputsUnavailable",
        &BuildFailureKind::InputsUnavailable,
    );
    ser(
        out_dir,
        "BuildFailureKind_CorruptEvalCache",
        &BuildFailureKind::CorruptEvalCache,
    );

    // ── Cache / candidate types ──────────────────────────────────────────────
    ser(
        out_dir,
        "CachedPath_minimal",
        &CachedPath {
            path: "/nix/store/dddddddddddddddddddddddddddddddd-foo".into(),
            cached: false,
            file_size: None,
            nar_size: None,
            url: None,
            nar_hash: None,
            file_hash: None,
            references: None,
            signatures: None,
            deriver: None,
            ca: None,
        },
    );
    ser(
        out_dir,
        "CachedPath_full",
        &CachedPath {
            path: "/nix/store/dddddddddddddddddddddddddddddddd-foo".into(),
            cached: true,
            file_size: Some(1000),
            nar_size: Some(2000),
            url: Some("https://s3.example.com/presigned?sig=abc".into()),
            nar_hash: Some("sha256:aaa".into()),
            file_hash: Some("sha256:bbb".into()),
            references: Some(vec!["/nix/store/eeee...-bar".into()]),
            signatures: Some(vec!["cache.nixos.org-1:abcd==".into()]),
            deriver: Some("/nix/store/ffff...-foo.drv".into()),
            ca: Some("fixed:r:sha256:ccc".into()),
        },
    );
    ser(
        out_dir,
        "QueryMode_Normal",
        &QueryMode::Normal,
    );
    ser(out_dir, "QueryMode_Pull", &QueryMode::Pull);
    ser(out_dir, "QueryMode_Push", &QueryMode::Push);
    ser(
        out_dir,
        "RequiredPath_no_cache_info",
        &RequiredPath {
            path: "/nix/store/gggg...-baz".into(),
            cache_info: None,
        },
    );
    ser(
        out_dir,
        "RequiredPath_with_cache_info",
        &RequiredPath {
            path: "/nix/store/gggg...-baz".into(),
            cache_info: Some(CacheInfo {
                file_size: 111,
                nar_size: 222,
            }),
        },
    );
    ser(
        out_dir,
        "JobCandidate_empty",
        &JobCandidate {
            job_id: "j-1".into(),
            required_paths: vec![],
            drv_paths: vec![],
        },
    );
    ser(
        out_dir,
        "JobCandidate_populated",
        &JobCandidate {
            job_id: "j-1".into(),
            required_paths: vec![RequiredPath {
                path: "/nix/store/gggg...-baz".into(),
                cache_info: None,
            }],
            drv_paths: vec!["/nix/store/hhhh...-baz.drv".into()],
        },
    );
    ser(
        out_dir,
        "CandidateScore",
        &CandidateScore {
            job_id: "j-1".into(),
            missing_count: 3,
            missing_nar_size: 987654321,
        },
    );

    // ── FailedPeer ────────────────────────────────────────────────────────
    ser(
        out_dir,
        "FailedPeer",
        &FailedPeer {
            peer_id: "peer-1".into(),
            reason: "invalid token".into(),
        },
    );

    // ── ClientMessage variants (build-only subset) ───────────────────────
    ser(
        out_dir,
        "ClientMessage_InitConnection",
        &ClientMessage::InitConnection {
            version: 1,
            capabilities: GradientCapabilities {
                build: true,
                ..Default::default()
            },
            id: "550e8400-e29b-41d4-a716-446655440000".into(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_AuthResponse_empty",
        &ClientMessage::AuthResponse { tokens: vec![] },
    );
    ser(
        out_dir,
        "ClientMessage_AuthResponse_tokens",
        &ClientMessage::AuthResponse {
            tokens: vec![
                ("peer-1".into(), "plaintext-token-1".into()),
                ("*".into(), "wildcard-token".into()),
            ],
        },
    );
    ser(out_dir, "ClientMessage_ReauthRequest", &ClientMessage::ReauthRequest);
    ser(
        out_dir,
        "ClientMessage_Reject",
        &ClientMessage::Reject {
            code: 400,
            reason: "bad request".into(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_WorkerCapabilities",
        &ClientMessage::WorkerCapabilities {
            architectures: vec!["x86_64-linux".into(), "aarch64-linux".into()],
            system_features: vec!["big-parallel".into()],
            max_concurrent_builds: 5,
            cpu_count: 16,
            ram_total_mb: 65536,
            cpu_core_score: 1200,
        },
    );
    ser(
        out_dir,
        "ClientMessage_WorkerMetrics",
        &ClientMessage::WorkerMetrics {
            cpu_usage_pct: 42.5,
            ram_free_mb: 4096,
            disk_speed_mbps: Some(500.0),
            network_speed_mbps: None,
        },
    );
    ser(out_dir, "ClientMessage_RequestJobList", &ClientMessage::RequestJobList);
    ser(
        out_dir,
        "ClientMessage_AssignJobResponse_accept",
        &ClientMessage::AssignJobResponse {
            job_id: "j-1".into(),
            accepted: true,
            reason: None,
        },
    );
    ser(
        out_dir,
        "ClientMessage_AssignJobResponse_reject",
        &ClientMessage::AssignJobResponse {
            job_id: "j-1".into(),
            accepted: false,
            reason: Some("no capacity".into()),
        },
    );
    ser(
        out_dir,
        "ClientMessage_JobUpdate_Building",
        &ClientMessage::JobUpdate {
            job_id: "j-1".into(),
            update: JobUpdateKind::Building {
                build_id: "b-1".into(),
            },
        },
    );
    ser(
        out_dir,
        "ClientMessage_JobUpdate_BuildOutput",
        &ClientMessage::JobUpdate {
            job_id: "j-1".into(),
            update: JobUpdateKind::BuildOutput {
                build_id: "b-1".into(),
                outputs: vec![build_output.clone()],
                metrics: Some(build_metrics.clone()),
                substituted: false,
            },
        },
    );
    ser(
        out_dir,
        "ClientMessage_JobCompleted",
        &ClientMessage::JobCompleted {
            job_id: "j-1".into(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_JobFailed",
        &ClientMessage::JobFailed {
            job_id: "j-1".into(),
            error: "build failed".into(),
            kind: BuildFailureKind::Permanent,
            missing_paths: vec![],
        },
    );
    ser(
        out_dir,
        "ClientMessage_JobFailed_InputsUnavailable",
        &ClientMessage::JobFailed {
            job_id: "j-1".into(),
            error: "missing inputs".into(),
            kind: BuildFailureKind::InputsUnavailable,
            missing_paths: vec!["/nix/store/iiii...-missing".into()],
        },
    );
    ser(out_dir, "ClientMessage_Draining", &ClientMessage::Draining);
    ser(
        out_dir,
        "ClientMessage_LogChunk",
        &ClientMessage::LogChunk {
            job_id: "j-1".into(),
            task_index: 0,
            data: b"building hello-2.12...\n".to_vec(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_LogChunk_empty",
        &ClientMessage::LogChunk {
            job_id: "j-1".into(),
            task_index: 2,
            data: vec![],
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarRequest",
        &ClientMessage::NarRequest {
            job_id: "j-1".into(),
            paths: vec!["/nix/store/jjjj...-a".into(), "/nix/store/kkkk...-b".into()],
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarPush",
        &ClientMessage::NarPush {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            data: vec![0u8, 1, 2, 3, 255, 254, 253],
            offset: 0,
            is_final: false,
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarPush_final",
        &ClientMessage::NarPush {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            data: vec![9, 9, 9],
            offset: 4194304,
            is_final: true,
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarUploaded",
        &ClientMessage::NarUploaded {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            file_hash: "sha256:aaaa".into(),
            file_size: 1234,
            nar_size: 5678,
            nar_hash: "sha256:bbbb".into(),
            references: vec!["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into()],
            deriver: Some("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-hello-2.12.drv".into()),
            ca: Some(
                "text:sha256:006vc8gixyrcynsx4lz1qxingl0mdja3l0xw1nl0j73isg37x944".into(),
            ),
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarUploaded_minimal",
        &ClientMessage::NarUploaded {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            file_hash: "sha256:aaaa".into(),
            file_size: 0,
            nar_size: 0,
            nar_hash: "sha256:bbbb".into(),
            references: vec![],
            deriver: None,
            ca: None,
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarStreamHeader",
        &ClientMessage::NarStreamHeader {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            total_bytes: Some(5678),
            stream_token: "tok-1".into(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_NarRequestResume",
        &ClientMessage::NarRequestResume {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            received_bytes: 2048,
            stream_token: "tok-1".into(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_RequestJob_Build",
        &ClientMessage::RequestJob {
            kind: JobKind::Build,
        },
    );
    ser(
        out_dir,
        "ClientMessage_RequestAllCandidates",
        &ClientMessage::RequestAllCandidates,
    );
    ser(
        out_dir,
        "ClientMessage_CacheQuery",
        &ClientMessage::CacheQuery {
            job_id: "j-1".into(),
            query_id: "q-1".into(),
            paths: vec!["/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into()],
            mode: QueryMode::Push,
        },
    );
    ser(
        out_dir,
        "ClientMessage_EvalMessage",
        &ClientMessage::EvalMessage {
            job_id: "j-1".into(),
            level: EvalMessageLevel::Warning,
            source: "build-prefetch".into(),
            message: "slow substitution".into(),
        },
    );
    ser(
        out_dir,
        "ClientMessage_QueryKnownDerivations",
        &ClientMessage::QueryKnownDerivations {
            job_id: "j-1".into(),
            drv_paths: vec!["/nix/store/llll...-a.drv".into()],
        },
    );

    // ── ServerMessage variants (build-only subset) ───────────────────────
    ser(
        out_dir,
        "ServerMessage_AuthChallenge_empty",
        &ServerMessage::AuthChallenge { peers: vec![] },
    );
    ser(
        out_dir,
        "ServerMessage_AuthChallenge_peers",
        &ServerMessage::AuthChallenge {
            peers: vec!["peer-1".into(), "peer-2".into()],
        },
    );
    ser(
        out_dir,
        "ServerMessage_InitAck",
        &ServerMessage::InitAck {
            version: 1,
            capabilities: GradientCapabilities {
                core: true,
                build: true,
                cache: true,
                ..Default::default()
            },
            authorized_peers: vec!["peer-1".into()],
            failed_peers: vec![FailedPeer {
                peer_id: "peer-2".into(),
                reason: "bad token".into(),
            }],
        },
    );
    ser(
        out_dir,
        "ServerMessage_Reject",
        &ServerMessage::Reject {
            code: 401,
            reason: "unauthorized".into(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_Error",
        &ServerMessage::Error {
            code: 500,
            message: "internal error".into(),
        },
    );
    ser(out_dir, "ServerMessage_Draining", &ServerMessage::Draining);
    ser(
        out_dir,
        "ServerMessage_AssignJob_Build",
        &ServerMessage::AssignJob {
            job_id: "j-1".into(),
            job: job_build_three.clone(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_AbortJob",
        &ServerMessage::AbortJob {
            job_id: "j-1".into(),
            reason: "cancelled by user".into(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_Credential",
        &ServerMessage::Credential {
            kind: CredentialKind::SshKey,
            data: b"-----BEGIN OPENSSH PRIVATE KEY-----\n...".to_vec(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_NarPush",
        &ServerMessage::NarPush {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            data: vec![7, 8, 9],
            offset: 0,
            is_final: true,
        },
    );
    ser(
        out_dir,
        "ServerMessage_NarUnavailable",
        &ServerMessage::NarUnavailable {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            reason: "not in cache".into(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_NarAbort",
        &ServerMessage::NarAbort {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            reason: "write failed".into(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_NarStreamHeader",
        &ServerMessage::NarStreamHeader {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            total_bytes: 5678,
            stream_token: "tok-1".into(),
        },
    );
    ser(
        out_dir,
        "ServerMessage_NarPushResume",
        &ServerMessage::NarPushResume {
            job_id: "j-1".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            received_bytes: 2048,
        },
    );
    ser(
        out_dir,
        "ServerMessage_RequestAllScores",
        &ServerMessage::RequestAllScores,
    );
    ser(
        out_dir,
        "ServerMessage_CacheStatus_empty",
        &ServerMessage::CacheStatus {
            query_id: "q-1".into(),
            cached: vec![],
        },
    );
    ser(
        out_dir,
        "ServerMessage_CacheStatus_populated",
        &ServerMessage::CacheStatus {
            query_id: "q-1".into(),
            cached: vec![
                CachedPath {
                    path: "/nix/store/dddddddddddddddddddddddddddddddd-foo".into(),
                    cached: true,
                    file_size: Some(1000),
                    nar_size: Some(2000),
                    url: Some("https://s3.example.com/presigned?sig=abc".into()),
                    nar_hash: Some("sha256:aaa".into()),
                    file_hash: Some("sha256:bbb".into()),
                    references: Some(vec![]),
                    signatures: Some(vec![]),
                    deriver: None,
                    ca: None,
                },
            ],
        },
    );
    ser(
        out_dir,
        "ServerMessage_KnownDerivations",
        &ServerMessage::KnownDerivations {
            job_id: "j-1".into(),
            known: vec!["/nix/store/mmmm...-a.drv".into()],
        },
    );
    ser(
        out_dir,
        "ServerMessage_CacheError",
        &ServerMessage::CacheError {
            query_id: "q-1".into(),
            message: "db timeout".into(),
        },
    );

    // ── Bisection: JobUpdateKind::BuildOutput with empty outputs + Some(metrics) + true ──
    ser(
        out_dir,
        "bisect_JobUpdateKind_metrics_substituted",
        &JobUpdateKind::BuildOutput {
            build_id: "b-1".into(),
            outputs: vec![],
            metrics: Some(BuildMetrics {
                peak_ram_mb: Some(512),
                cpu_time_ms: None,
                avg_cpu_pct: None,
                disk_read_bytes: None,
                disk_write_bytes: None,
                oom_killed: false,
                build_time_ms: Some(12345),
                peak_network_mbps: None,
            }),
            substituted: true,
        },
    );

    // ── Bisection: BuildOutput with two out-of-line strings + empty vec ──
    ser(
        out_dir,
        "bisect_BuildOutput",
        &BuildOutput {
            name: "n".into(),
            store_path: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12".into(),
            hash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
                .into(),
            nar_size: None,
            nar_hash: None,
            products: vec![],
        },
    );

    println!("\nDone. Fixtures written under {}", out_dir.display());
}
