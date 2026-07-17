use rkyv::rancor::Error as RkyvError;
use rkyv_fixtures::messages::{ClientMessage, FailedPeer, ServerMessage};
use rkyv_fixtures::types::{
    BuildJob, BuildMetrics, BuildOutput, BuildProduct, BuildTask,
    GradientCapabilities, Job, JobUpdateKind,
};
use std::env;
use std::fs;

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 3 {
        eprintln!("usage: verify <type> <path.bin>");
        std::process::exit(2);
    }
    let ty = &args[1];
    let path = &args[2];
    let bytes = fs::read(path).expect("read file");

    macro_rules! check {
        ($t:ty) => {{
            match rkyv::access::<<$t as rkyv::Archive>::Archived, RkyvError>(&bytes) {
                Ok(archived) => {
                    println!("OK: {} bytes validated as {}", bytes.len(), stringify!($t));
                    println!("{:#?}", archived);
                }
                Err(e) => {
                    println!("INVALID: {}", e);
                    std::process::exit(1);
                }
            }
        }};
    }

    match ty.as_str() {
        "ClientMessage" => check!(ClientMessage),
        "ServerMessage" => check!(ServerMessage),
        "Job" => check!(Job),
        "JobUpdateKind" => check!(JobUpdateKind),
        "BuildTask" => check!(BuildTask),
        "BuildJob" => check!(BuildJob),
        "BuildOutput" => check!(BuildOutput),
        "BuildProduct" => check!(BuildProduct),
        "BuildMetrics" => check!(BuildMetrics),
        "FailedPeer" => check!(FailedPeer),
        "GradientCapabilities" => check!(GradientCapabilities),
        other => {
            eprintln!("unknown type {other}");
            std::process::exit(2);
        }
    }
}
