#![forbid(unsafe_code)]

use std::{env, path::PathBuf};

fn main() -> Result<(), pump19_adaptations::AdaptationError> {
    let root = env::args_os().nth(1).map_or_else(
        || PathBuf::from("examples/deployment/adaptations"),
        PathBuf::from,
    );
    pump19_adaptations::write_baseline_deployment_packs(&root)?;
    Ok(())
}
