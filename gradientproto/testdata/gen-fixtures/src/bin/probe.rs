use rkyv_fixtures::messages;
use rkyv_fixtures::types;
use rkyv::rancor::Error as RkyvError;
use std::fs;
use std::path::Path;

fn main() {
    print_aligns();
    print_sizes();
    let out_dir = Path::new("fixtures/probe");
    fs::create_dir_all(out_dir).expect("mkdir");

    for len in [0usize, 1, 3, 6, 7, 8, 9, 10, 11, 12, 13, 15, 16, 20, 100, 126, 127, 128, 129, 130, 200, 255, 256, 257, 300, 65535, 65536, 65537] {
        let s: String = "x".repeat(len);
        let bytes = rkyv::to_bytes::<RkyvError>(&s).expect("ser");
        let path = out_dir.join(format!("str_{len:06}.bin"));
        fs::write(&path, &bytes).expect("write");
        println!("len={len}: archive={} bytes -> {}", bytes.len(), path.display());
    }

    // Also probe Vec<T> and Option<T> at varying sizes to nail those reprs.
    for len in [0usize, 1, 2, 3, 4, 300] {
        let v: Vec<u32> = (0..len as u32).collect();
        let bytes = rkyv::to_bytes::<RkyvError>(&v).expect("ser");
        let path = out_dir.join(format!("vecu32_{len:06}.bin"));
        fs::write(&path, &bytes).expect("write");
        println!("vec<u32> len={len}: archive={} bytes -> {}", bytes.len(), path.display());
    }

    let none: Option<u32> = None;
    let some: Option<u32> = Some(0x11223344);
    fs::write(out_dir.join("option_u32_none.bin"), rkyv::to_bytes::<RkyvError>(&none).unwrap()).unwrap();
    fs::write(out_dir.join("option_u32_some.bin"), rkyv::to_bytes::<RkyvError>(&some).unwrap()).unwrap();

    let none_s: Option<String> = None;
    let some_s: Option<String> = Some("hi".into());
    fs::write(out_dir.join("option_string_none.bin"), rkyv::to_bytes::<RkyvError>(&none_s).unwrap()).unwrap();
    fs::write(out_dir.join("option_string_some.bin"), rkyv::to_bytes::<RkyvError>(&some_s).unwrap()).unwrap();

    println!("done");
}

#[allow(dead_code)]
fn print_sizes() {
    use rkyv::Archive;
    println!("size_of::<Archived<ClientMessage>>() = {}", std::mem::size_of::<<messages::ClientMessage as Archive>::Archived>());
    println!("size_of::<Archived<ServerMessage>>() = {}", std::mem::size_of::<<messages::ServerMessage as Archive>::Archived>());
    println!("size_of::<Archived<Job>>() = {}", std::mem::size_of::<<types::Job as Archive>::Archived>());
    println!("size_of::<Archived<JobUpdateKind>>() = {}", std::mem::size_of::<<types::JobUpdateKind as Archive>::Archived>());
}

fn print_aligns() {
    use std::mem::align_of;
    println!("align_of::<Archived<ClientMessage>>() = {}", align_of::<<messages::ClientMessage as rkyv::Archive>::Archived>());
    println!("align_of::<Archived<ServerMessage>>() = {}", align_of::<<messages::ServerMessage as rkyv::Archive>::Archived>());
    println!("align_of::<Archived<Job>>() = {}", align_of::<<types::Job as rkyv::Archive>::Archived>());
    println!("align_of::<Archived<JobUpdateKind>>() = {}", align_of::<<types::JobUpdateKind as rkyv::Archive>::Archived>());
}
