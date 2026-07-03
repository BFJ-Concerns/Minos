#![forbid(unsafe_code)]

use std::path::PathBuf;

fn main() {
    if let Err(error) = run() {
        eprintln!("{error}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), pump19_daemon::DaemonError> {
    let config_path = std::env::args_os()
        .nth(1)
        .map(PathBuf::from)
        .ok_or(pump19_daemon::DaemonError::MissingConfigPath)?;
    let config = pump19_daemon::DaemonConfig::load(&config_path)?;
    pump19_daemon::run_from_config(config)
}
