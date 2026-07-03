#![forbid(unsafe_code)]
#![cfg_attr(
    test,
    allow(
        clippy::expect_used,
        clippy::unwrap_used,
        reason = "unit tests use small fakes and direct fixture assertions"
    )
)]

use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::{Command, ExitStatus, Stdio},
};

use pump19_contract::RunKind;
use pump19_core::{
    CoreError, PreparedSource, WorkspaceExecOutput, WorkspaceExecRequest, WorkspaceIsolation,
    WorkspaceLease, WorkspaceProvider, WorkspaceRequest,
};
use thiserror::Error;

const DEFAULT_CONTAINER_WORKDIR: &str = "/workspace";
const DEFAULT_HOME: &str = "/workspace/home";
const DEFAULT_SHELL: &str = "/bin/sh";
const DEFAULT_PATH: &str = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";
const RUNTIME_PATH: &str = "/usr/bin/podman";

/// Errors raised while preparing or cleaning an isolated workspace.
#[derive(Debug, Error)]
pub enum WorkspaceError {
    #[error("workspace root {path} could not be created: {source}")]
    CreateRoot {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("workspace root {path} could not be removed: {source}")]
    RemoveRoot {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("workspace configuration is not isolating: {0}")]
    WeakConfiguration(String),
    #[error("container runtime failed: {0}")]
    Runtime(String),
}

/// Runtime boundary used by the provider.
pub trait ContainerRuntime {
    /// Creates the configured container workspace.
    ///
    /// # Errors
    ///
    /// Returns an error when the runtime cannot create the container with the
    /// requested isolation settings.
    fn create(&mut self, spec: &ContainerSpec) -> Result<(), WorkspaceError>;

    /// Executes a command inside a running workspace container.
    ///
    /// # Errors
    ///
    /// Returns an error when the runtime cannot start or collect the command.
    fn exec(
        &mut self,
        container_id: &str,
        request: &WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, WorkspaceError>;

    /// Copies a trusted host-side source tree into the running container.
    ///
    /// # Errors
    ///
    /// Returns an error when the runtime cannot copy the source tree through the
    /// container boundary.
    fn copy_into(
        &mut self,
        container_id: &str,
        host_source: &Path,
        container_dest: &str,
    ) -> Result<(), WorkspaceError>;

    /// Removes the configured container workspace.
    ///
    /// # Errors
    ///
    /// Returns an error when the runtime cannot remove the container.
    fn remove(&mut self, container_id: &str) -> Result<(), WorkspaceError>;
}

/// Command-line OCI runtime implementation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CommandRuntime {
    binary: PathBuf,
}

impl Default for CommandRuntime {
    fn default() -> Self {
        Self {
            binary: PathBuf::from(RUNTIME_PATH),
        }
    }
}

impl CommandRuntime {
    /// Creates a runtime backed by the supplied binary path.
    #[must_use]
    pub fn new(binary: impl Into<PathBuf>) -> Self {
        Self {
            binary: binary.into(),
        }
    }

    fn command(&self) -> Command {
        let mut command = Command::new(&self.binary);
        // The container CLI is trusted, but it must not inherit forge tokens,
        // SSH agent sockets, or tool-specific credential variables from Pump-19.
        command.env_clear();
        command
    }
}

impl ContainerRuntime for CommandRuntime {
    fn create(&mut self, spec: &ContainerSpec) -> Result<(), WorkspaceError> {
        let mut command = self.command();
        command.args(create_args(spec));
        run_command(command, "create workspace container")
    }

    fn exec(
        &mut self,
        container_id: &str,
        request: &WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, WorkspaceError> {
        let mut command = self.command();
        command.args(exec_args(container_id, request));
        command.stdin(Stdio::piped());
        command.stdout(Stdio::piped());
        command.stderr(Stdio::piped());
        let mut child = command
            .spawn()
            .map_err(|source| WorkspaceError::Runtime(format!("execute in workspace: {source}")))?;
        if let Some(stdin) = child.stdin.as_mut() {
            stdin.write_all(&request.stdin).map_err(|source| {
                WorkspaceError::Runtime(format!("write workspace stdin: {source}"))
            })?;
        }
        let output = child.wait_with_output().map_err(|source| {
            WorkspaceError::Runtime(format!("collect workspace output: {source}"))
        })?;
        Ok(WorkspaceExecOutput {
            exit_code: output.status.code().unwrap_or(-1),
            stdout: output.stdout,
            stderr: output.stderr,
        })
    }

    fn copy_into(
        &mut self,
        container_id: &str,
        host_source: &Path,
        container_dest: &str,
    ) -> Result<(), WorkspaceError> {
        let mut command = self.command();
        let source = format!("{}/.", host_source.display());
        let destination = format!("{container_id}:{container_dest}/");
        command.args(["cp", &source, &destination]);
        run_command(command, "copy source into workspace container")
    }

    fn remove(&mut self, container_id: &str) -> Result<(), WorkspaceError> {
        let mut command = self.command();
        command.args(["rm", "--force", "--volumes", container_id]);
        run_command(command, "remove workspace container")
    }
}

fn run_command(mut command: Command, action: &str) -> Result<(), WorkspaceError> {
    let output = command
        .output()
        .map_err(|source| WorkspaceError::Runtime(format!("{action}: {source}")))?;
    if output.status.success() {
        return Ok(());
    }
    Err(WorkspaceError::Runtime(format!(
        "{action}: exited with {}: {}",
        status_text(output.status),
        String::from_utf8_lossy(&output.stderr)
    )))
}

fn status_text(status: ExitStatus) -> String {
    status
        .code()
        .map_or_else(|| "signal".to_owned(), |code| code.to_string())
}

/// Provider configuration for one class of isolated workspaces.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WorkspaceConfig {
    pub root: PathBuf,
    pub image: String,
    pub resources: ResourceLimits,
    pub network: NetworkPolicy,
    pub security: SecurityProfile,
}

impl WorkspaceConfig {
    /// Creates a locked-down workspace configuration.
    #[must_use]
    pub fn new(root: impl Into<PathBuf>, image: impl Into<String>) -> Self {
        Self {
            root: root.into(),
            image: image.into(),
            resources: ResourceLimits::default(),
            network: NetworkPolicy::default(),
            security: SecurityProfile::default(),
        }
    }

    fn validate(&self) -> Result<(), WorkspaceError> {
        if self.image.trim().is_empty() {
            return Err(WorkspaceError::WeakConfiguration(
                "container image must be explicit".to_owned(),
            ));
        }
        if self.network != NetworkPolicy::Disabled {
            return Err(WorkspaceError::WeakConfiguration(
                "network egress must be disabled by default".to_owned(),
            ));
        }
        self.resources.validate()?;
        self.security.validate()
    }
}

/// Resource caps applied to untrusted PR-code execution.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct ResourceLimits {
    pub memory_bytes: u64,
    pub cpu_period_micros: u64,
    pub cpu_quota_micros: u64,
    pub pids_limit: u32,
    pub workspace_bytes: u64,
    pub tmp_bytes: u64,
}

impl Default for ResourceLimits {
    fn default() -> Self {
        Self {
            memory_bytes: 2 * 1024 * 1024 * 1024,
            cpu_period_micros: 100_000,
            cpu_quota_micros: 200_000,
            pids_limit: 512,
            workspace_bytes: 8 * 1024 * 1024 * 1024,
            tmp_bytes: 1024 * 1024 * 1024,
        }
    }
}

impl ResourceLimits {
    fn validate(self) -> Result<(), WorkspaceError> {
        if self.memory_bytes == 0 {
            return Err(WorkspaceError::WeakConfiguration(
                "memory limit must be non-zero".to_owned(),
            ));
        }
        if self.cpu_period_micros == 0 || self.cpu_quota_micros == 0 {
            return Err(WorkspaceError::WeakConfiguration(
                "CPU quota and period must be non-zero".to_owned(),
            ));
        }
        if self.pids_limit == 0 {
            return Err(WorkspaceError::WeakConfiguration(
                "PID limit must be non-zero".to_owned(),
            ));
        }
        if self.workspace_bytes == 0 || self.tmp_bytes == 0 {
            return Err(WorkspaceError::WeakConfiguration(
                "workspace and tmpfs limits must be non-zero".to_owned(),
            ));
        }
        Ok(())
    }
}

/// Network policy for untrusted code.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub enum NetworkPolicy {
    #[default]
    Disabled,
}

/// Security controls expected from the container runtime.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct SecurityProfile {
    pub root_filesystem: RootFilesystem,
    pub capabilities: CapabilityPolicy,
    pub privilege: PrivilegeMode,
    pub pid_namespace: NamespacePolicy,
    pub ipc_namespace: NamespacePolicy,
}

impl Default for SecurityProfile {
    fn default() -> Self {
        Self {
            root_filesystem: RootFilesystem::ReadOnly,
            capabilities: CapabilityPolicy::DropAll,
            privilege: PrivilegeMode::NoNewPrivileges,
            pid_namespace: NamespacePolicy::Private,
            ipc_namespace: NamespacePolicy::Private,
        }
    }
}

impl SecurityProfile {
    fn validate(self) -> Result<(), WorkspaceError> {
        if self.root_filesystem != RootFilesystem::ReadOnly {
            return Err(WorkspaceError::WeakConfiguration(
                "container root filesystem must be read-only".to_owned(),
            ));
        }
        if self.capabilities != CapabilityPolicy::DropAll {
            return Err(WorkspaceError::WeakConfiguration(
                "container capabilities must be dropped".to_owned(),
            ));
        }
        if self.privilege != PrivilegeMode::NoNewPrivileges {
            return Err(WorkspaceError::WeakConfiguration(
                "no-new-privileges must be enabled".to_owned(),
            ));
        }
        if self.pid_namespace != NamespacePolicy::Private
            || self.ipc_namespace != NamespacePolicy::Private
        {
            return Err(WorkspaceError::WeakConfiguration(
                "host namespaces and privileged mode are forbidden".to_owned(),
            ));
        }
        Ok(())
    }
}

/// Root filesystem mode for the workspace container.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum RootFilesystem {
    ReadOnly,
    Writable,
}

/// Linux capability policy for the workspace container.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum CapabilityPolicy {
    DropAll,
    RuntimeDefault,
}

/// Privilege mode for the workspace container.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum PrivilegeMode {
    NoNewPrivileges,
    Privileged,
}

/// Namespace policy for the workspace container.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum NamespacePolicy {
    Private,
    Host,
}

/// Complete container configuration emitted by the provider.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ContainerSpec {
    pub id: String,
    pub image: String,
    pub host_control_dir: PathBuf,
    pub workdir: String,
    pub env: BTreeMap<String, String>,
    pub resources: ResourceLimits,
    pub network: NetworkPolicy,
    pub security: SecurityProfile,
}

impl ContainerSpec {
    fn new(
        config: &WorkspaceConfig,
        request: &WorkspaceRequest,
        host_control_dir: PathBuf,
    ) -> Self {
        Self {
            id: container_id(request),
            image: config.image.clone(),
            host_control_dir,
            workdir: DEFAULT_CONTAINER_WORKDIR.to_owned(),
            env: credential_free_env(),
            resources: config.resources,
            network: config.network,
            security: config.security,
        }
    }

    fn validate_for_present_isolation(&self) -> Result<(), WorkspaceError> {
        if self.network != NetworkPolicy::Disabled {
            return Err(WorkspaceError::WeakConfiguration(
                "workspace container must disable network egress".to_owned(),
            ));
        }
        if has_credential_env(&self.env) {
            return Err(WorkspaceError::WeakConfiguration(
                "workspace environment contains credential-shaped variables".to_owned(),
            ));
        }
        self.resources.validate()?;
        self.security.validate()
    }
}

/// Container-backed implementation of the core `WorkspaceProvider` seam.
#[derive(Debug)]
pub struct ContainerWorkspaceProvider<R> {
    config: WorkspaceConfig,
    runtime: R,
}

impl ContainerWorkspaceProvider<CommandRuntime> {
    /// Creates a provider using `/usr/bin/podman`.
    #[must_use]
    pub fn new(config: WorkspaceConfig) -> Self {
        Self::with_runtime(config, CommandRuntime::default())
    }
}

impl<R> ContainerWorkspaceProvider<R>
where
    R: ContainerRuntime,
{
    /// Creates a provider using an injected container runtime.
    #[must_use]
    pub const fn with_runtime(config: WorkspaceConfig, runtime: R) -> Self {
        Self { config, runtime }
    }

    /// Returns the configured runtime.
    #[must_use]
    pub const fn runtime(&self) -> &R {
        &self.runtime
    }

    /// Removes the runtime container and the host-side lease directory.
    ///
    /// # Errors
    ///
    /// Returns an error when either the container or the host-side lease directory
    /// cannot be removed.
    pub fn cleanup(&mut self, lease: &WorkspaceLease) -> Result<(), WorkspaceError> {
        self.runtime.remove(&lease.id)?;
        if lease.root.exists() {
            fs::remove_dir_all(&lease.root).map_err(|source| WorkspaceError::RemoveRoot {
                path: lease.root.display().to_string(),
                source,
            })?;
        }
        Ok(())
    }
}

impl<R> WorkspaceProvider for ContainerWorkspaceProvider<R>
where
    R: ContainerRuntime,
{
    fn prepare(&mut self, request: WorkspaceRequest) -> Result<WorkspaceLease, CoreError> {
        self.prepare_container(&request)
            .map_err(|error| CoreError::Workspace(error.to_string()))
    }

    fn inject_source(
        &mut self,
        lease: &WorkspaceLease,
        source: &PreparedSource,
    ) -> Result<(), CoreError> {
        self.inject_source_tree(lease, source)
            .map_err(|error| CoreError::Workspace(error.to_string()))
    }

    fn exec(
        &mut self,
        lease: &WorkspaceLease,
        request: WorkspaceExecRequest,
    ) -> Result<WorkspaceExecOutput, CoreError> {
        self.runtime
            .exec(&lease.id, &request)
            .map_err(|error| CoreError::Workspace(error.to_string()))
    }

    fn cleanup(&mut self, lease: &WorkspaceLease) -> Result<(), CoreError> {
        Self::cleanup(self, lease).map_err(|error| CoreError::Workspace(error.to_string()))
    }
}

impl<R> ContainerWorkspaceProvider<R>
where
    R: ContainerRuntime,
{
    fn prepare_container(
        &mut self,
        request: &WorkspaceRequest,
    ) -> Result<WorkspaceLease, WorkspaceError> {
        self.config.validate()?;
        let host_control_dir = self.config.root.join(container_id(request));
        fs::create_dir_all(&host_control_dir).map_err(|source| WorkspaceError::CreateRoot {
            path: host_control_dir.display().to_string(),
            source,
        })?;

        let spec = ContainerSpec::new(&self.config, request, host_control_dir.clone());
        spec.validate_for_present_isolation()?;
        if let Err(error) = self.runtime.create(&spec) {
            remove_host_control_dir(&host_control_dir)?;
            return Err(error);
        }

        Ok(WorkspaceLease {
            id: spec.id,
            root: host_control_dir,
            isolation: WorkspaceIsolation {
                isolated: true,
                credential_free: true,
                egress_bounded: true,
                resource_bounded: true,
                ephemeral: true,
            },
        })
    }

    fn inject_source_tree(
        &mut self,
        lease: &WorkspaceLease,
        source: &PreparedSource,
    ) -> Result<(), WorkspaceError> {
        clear_directory(&lease.root)?;
        copy_directory_contents(&source.tree, &lease.root)?;
        self.runtime
            .copy_into(&lease.id, &lease.root, DEFAULT_CONTAINER_WORKDIR)
    }
}

fn remove_host_control_dir(path: &Path) -> Result<(), WorkspaceError> {
    if path.exists() {
        fs::remove_dir_all(path).map_err(|source| WorkspaceError::RemoveRoot {
            path: path.display().to_string(),
            source,
        })?;
    }
    Ok(())
}

fn clear_directory(path: &Path) -> Result<(), WorkspaceError> {
    if !path.exists() {
        fs::create_dir_all(path).map_err(|source| WorkspaceError::CreateRoot {
            path: path.display().to_string(),
            source,
        })?;
        return Ok(());
    }
    for entry in fs::read_dir(path).map_err(|source| WorkspaceError::RemoveRoot {
        path: path.display().to_string(),
        source,
    })? {
        let entry = entry.map_err(|source| WorkspaceError::RemoveRoot {
            path: path.display().to_string(),
            source,
        })?;
        let entry_path = entry.path();
        let metadata =
            fs::symlink_metadata(&entry_path).map_err(|source| WorkspaceError::RemoveRoot {
                path: entry_path.display().to_string(),
                source,
            })?;
        if metadata.is_dir() && !metadata.file_type().is_symlink() {
            fs::remove_dir_all(&entry_path).map_err(|source| WorkspaceError::RemoveRoot {
                path: entry_path.display().to_string(),
                source,
            })?;
        } else {
            fs::remove_file(&entry_path).map_err(|source| WorkspaceError::RemoveRoot {
                path: entry_path.display().to_string(),
                source,
            })?;
        }
    }
    Ok(())
}

fn copy_directory_contents(source: &Path, destination: &Path) -> Result<(), WorkspaceError> {
    fs::create_dir_all(destination).map_err(|source_error| WorkspaceError::CreateRoot {
        path: destination.display().to_string(),
        source: source_error,
    })?;
    for entry in fs::read_dir(source).map_err(|source_error| WorkspaceError::CreateRoot {
        path: source.display().to_string(),
        source: source_error,
    })? {
        let entry = entry.map_err(|source_error| WorkspaceError::CreateRoot {
            path: source.display().to_string(),
            source: source_error,
        })?;
        let source_path = entry.path();
        let destination_path = destination.join(entry.file_name());
        let metadata = fs::symlink_metadata(&source_path).map_err(|source_error| {
            WorkspaceError::CreateRoot {
                path: source_path.display().to_string(),
                source: source_error,
            }
        })?;
        if metadata.file_type().is_symlink() {
            let target =
                fs::read_link(&source_path).map_err(|source_error| WorkspaceError::CreateRoot {
                    path: source_path.display().to_string(),
                    source: source_error,
                })?;
            #[cfg(unix)]
            std::os::unix::fs::symlink(&target, &destination_path).map_err(|source_error| {
                WorkspaceError::CreateRoot {
                    path: destination_path.display().to_string(),
                    source: source_error,
                }
            })?;
            #[cfg(not(unix))]
            fs::write(&destination_path, target.to_string_lossy().as_bytes()).map_err(
                |source_error| WorkspaceError::CreateRoot {
                    path: destination_path.display().to_string(),
                    source: source_error,
                },
            )?;
        } else if metadata.is_dir() {
            copy_directory_contents(&source_path, &destination_path)?;
        } else {
            fs::copy(&source_path, &destination_path).map_err(|source_error| {
                WorkspaceError::CreateRoot {
                    path: destination_path.display().to_string(),
                    source: source_error,
                }
            })?;
        }
    }
    Ok(())
}

fn credential_free_env() -> BTreeMap<String, String> {
    BTreeMap::from([
        ("CARGO_HOME".to_owned(), "/workspace/.cargo".to_owned()),
        (
            "GIT_CONFIG_GLOBAL".to_owned(),
            "/workspace/.gitconfig".to_owned(),
        ),
        ("GIT_CONFIG_NOSYSTEM".to_owned(), "1".to_owned()),
        ("GIT_TERMINAL_PROMPT".to_owned(), "0".to_owned()),
        ("HOME".to_owned(), DEFAULT_HOME.to_owned()),
        ("NPM_CONFIG_CACHE".to_owned(), "/workspace/.npm".to_owned()),
        ("PATH".to_owned(), DEFAULT_PATH.to_owned()),
        ("SHELL".to_owned(), DEFAULT_SHELL.to_owned()),
    ])
}

fn has_credential_env(env: &BTreeMap<String, String>) -> bool {
    env.keys().any(|key| credential_key(key))
        || env.values().any(|value| {
            value.contains("/.ssh")
                || value.contains(".netrc")
                || value.contains("SSH_AUTH_SOCK")
                || value.contains("GIT_ASKPASS")
        })
}

fn credential_key(key: &str) -> bool {
    let upper = key.to_ascii_uppercase();
    upper.contains("TOKEN")
        || upper.contains("SECRET")
        || upper.contains("PASSWORD")
        || upper.contains("CREDENTIAL")
        || upper == "SSH_AUTH_SOCK"
        || upper == "GIT_ASKPASS"
        || upper == "NETRC"
}

fn container_id(request: &WorkspaceRequest) -> String {
    let mut id = format!(
        "pump19-{}-{}-{}",
        run_kind_slug(request.run_kind),
        request.run_id.0,
        request.commit_sha
    );
    id = sanitise_component(&id);
    id.truncate(63);
    id.trim_matches('-').to_owned()
}

const fn run_kind_slug(kind: RunKind) -> &'static str {
    match kind {
        RunKind::Review => "review",
        RunKind::Judge => "judge",
        RunKind::Fix => "fix",
        RunKind::Finish => "finish",
    }
}

fn sanitise_component(value: &str) -> String {
    value
        .chars()
        .map(|character| {
            if character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.') {
                character
            } else {
                '-'
            }
        })
        .collect()
}

fn create_args(spec: &ContainerSpec) -> Vec<String> {
    let mut args = vec![
        "run".to_owned(),
        "--detach".to_owned(),
        "--name".to_owned(),
        spec.id.clone(),
        "--network=none".to_owned(),
        "--read-only".to_owned(),
        "--cap-drop=ALL".to_owned(),
        "--security-opt=no-new-privileges".to_owned(),
        "--pids-limit".to_owned(),
        spec.resources.pids_limit.to_string(),
        "--memory".to_owned(),
        spec.resources.memory_bytes.to_string(),
        "--cpu-period".to_owned(),
        spec.resources.cpu_period_micros.to_string(),
        "--cpu-quota".to_owned(),
        spec.resources.cpu_quota_micros.to_string(),
        "--workdir".to_owned(),
        spec.workdir.clone(),
        "--tmpfs".to_owned(),
        tmpfs_arg(&spec.workdir, spec.resources.workspace_bytes),
        "--tmpfs".to_owned(),
        tmpfs_arg("/tmp", spec.resources.tmp_bytes),
    ];

    args.extend(env_args(&spec.env));
    args.extend([
        spec.image.clone(),
        "sleep".to_owned(),
        "infinity".to_owned(),
    ]);
    args
}

fn exec_args(container_id: &str, request: &WorkspaceExecRequest) -> Vec<String> {
    let mut args = vec![
        "exec".to_owned(),
        "--workdir".to_owned(),
        request.cwd_inside_container.clone(),
    ];
    args.extend(env_args(&request.env_delta));
    args.push(container_id.to_owned());
    args.push(request.program.clone());
    args.extend(request.args.iter().cloned());
    args
}

fn env_args(env: &BTreeMap<String, String>) -> Vec<String> {
    env.iter()
        .flat_map(|(key, value)| ["--env".to_owned(), format!("{key}={value}")])
        .collect()
}

fn tmpfs_arg(path: &str, size_bytes: u64) -> String {
    format!("{path}:rw,nosuid,nodev,size={size_bytes}")
}

#[cfg(test)]
mod tests {
    use std::cell::RefCell;

    use pump19_contract::{PullRequestRef, RunId};
    use pump19_core::WorkspaceProvider as _;
    use tempfile::TempDir;

    use super::*;

    #[derive(Default, Debug)]
    struct RecordingRuntime {
        created: RefCell<Vec<ContainerSpec>>,
        execs: RefCell<Vec<(String, WorkspaceExecRequest)>>,
        copies: RefCell<Vec<(String, PathBuf, String)>>,
        removed: RefCell<Vec<String>>,
        fail_create: Option<String>,
    }

    impl ContainerRuntime for RecordingRuntime {
        fn create(&mut self, spec: &ContainerSpec) -> Result<(), WorkspaceError> {
            if let Some(reason) = &self.fail_create {
                return Err(WorkspaceError::Runtime(reason.clone()));
            }
            self.created.borrow_mut().push(spec.clone());
            Ok(())
        }

        fn exec(
            &mut self,
            container_id: &str,
            request: &WorkspaceExecRequest,
        ) -> Result<WorkspaceExecOutput, WorkspaceError> {
            self.execs
                .borrow_mut()
                .push((container_id.to_owned(), request.clone()));
            Ok(WorkspaceExecOutput {
                exit_code: 0,
                stdout: b"ok".to_vec(),
                stderr: Vec::new(),
            })
        }

        fn copy_into(
            &mut self,
            container_id: &str,
            host_source: &Path,
            container_dest: &str,
        ) -> Result<(), WorkspaceError> {
            self.copies.borrow_mut().push((
                container_id.to_owned(),
                host_source.to_path_buf(),
                container_dest.to_owned(),
            ));
            Ok(())
        }

        fn remove(&mut self, container_id: &str) -> Result<(), WorkspaceError> {
            self.removed.borrow_mut().push(container_id.to_owned());
            Ok(())
        }
    }

    fn request() -> WorkspaceRequest {
        WorkspaceRequest {
            run_id: RunId("run:1".to_owned()),
            run_kind: RunKind::Review,
            pr: PullRequestRef {
                repository: "owner/repo".to_owned(),
                id: "19".to_owned(),
            },
            commit_sha: "abc123".to_owned(),
        }
    }

    fn provider(root: &Path) -> ContainerWorkspaceProvider<RecordingRuntime> {
        let config = WorkspaceConfig::new(root, "localhost/pump19-workspace:stable");
        ContainerWorkspaceProvider::with_runtime(config, RecordingRuntime::default())
    }

    #[test]
    fn prepare_returns_present_isolation_for_container_runtime_configuration() {
        let temp = TempDir::new().expect("temp dir");
        let mut provider = provider(temp.path());

        let lease = provider.prepare(request()).expect("prepare");

        assert!(lease.isolation.present());
        assert!(lease.root.exists());
        assert!(lease.root.starts_with(temp.path()));
        assert_eq!(provider.runtime().created.borrow().len(), 1);
    }

    #[test]
    fn inject_source_copies_plain_tree_to_host_root_and_container_workspace() {
        let temp = TempDir::new().expect("temp dir");
        let source = temp.path().join("source");
        fs::create_dir_all(source.join("src")).expect("create source");
        fs::write(source.join("src/lib.rs"), "pub fn answer() -> u8 { 19 }\n")
            .expect("write source");
        let mut provider = provider(temp.path());
        let lease = provider.prepare(request()).expect("prepare");
        fs::write(lease.root.join("stale"), "old").expect("write stale file");

        provider
            .inject_source(
                &lease,
                &PreparedSource {
                    tree: source,
                    revision: "abc123".to_owned(),
                    cleanup_root: None,
                },
            )
            .expect("inject source");

        assert_eq!(
            fs::read_to_string(lease.root.join("src/lib.rs")).expect("read injected source"),
            "pub fn answer() -> u8 { 19 }\n"
        );
        assert!(!lease.root.join("stale").exists());
        let copies = provider.runtime().copies.borrow();
        assert_eq!(copies.len(), 1);
        assert_eq!(copies[0].0, lease.id);
        assert_eq!(copies[0].1, lease.root);
        assert_eq!(copies[0].2, DEFAULT_CONTAINER_WORKDIR);
    }

    #[test]
    fn emitted_container_spec_is_networkless_resource_bounded_and_non_privileged() {
        let temp = TempDir::new().expect("temp dir");
        let mut provider = provider(temp.path());

        provider.prepare(request()).expect("prepare");

        let created = provider.runtime().created.borrow();
        let spec = created.first().expect("created spec");
        assert_eq!(spec.network, NetworkPolicy::Disabled);
        assert_eq!(spec.security.root_filesystem, RootFilesystem::ReadOnly);
        assert_eq!(spec.security.capabilities, CapabilityPolicy::DropAll);
        assert_eq!(spec.security.privilege, PrivilegeMode::NoNewPrivileges);
        assert_eq!(spec.security.pid_namespace, NamespacePolicy::Private);
        assert_eq!(spec.security.ipc_namespace, NamespacePolicy::Private);
        assert!(spec.resources.memory_bytes > 0);
        assert!(spec.resources.cpu_period_micros > 0);
        assert!(spec.resources.cpu_quota_micros > 0);
        assert!(spec.resources.pids_limit > 0);
        assert!(spec.resources.workspace_bytes > 0);
    }

    #[test]
    fn command_args_apply_isolation_network_and_resource_bounds() {
        let temp = TempDir::new().expect("temp dir");
        let spec = ContainerSpec::new(
            &WorkspaceConfig::new(temp.path(), "image"),
            &request(),
            temp.path().join("run"),
        );

        let args = create_args(&spec);

        assert!(args.contains(&"--network=none".to_owned()));
        assert!(args.contains(&"--read-only".to_owned()));
        assert!(args.contains(&"--cap-drop=ALL".to_owned()));
        assert!(args.contains(&"--security-opt=no-new-privileges".to_owned()));
        assert!(args.contains(&"--pids-limit".to_owned()));
        assert!(args.contains(&spec.resources.pids_limit.to_string()));
        assert!(args.contains(&"--memory".to_owned()));
        assert!(args.contains(&spec.resources.memory_bytes.to_string()));
        assert!(args.contains(&"--cpu-period".to_owned()));
        assert!(args.contains(&spec.resources.cpu_period_micros.to_string()));
        assert!(args.contains(&"--cpu-quota".to_owned()));
        assert!(args.contains(&spec.resources.cpu_quota_micros.to_string()));
        assert!(args.contains(&tmpfs_arg(
            DEFAULT_CONTAINER_WORKDIR,
            spec.resources.workspace_bytes
        )));
        assert!(args.contains(&tmpfs_arg("/tmp", spec.resources.tmp_bytes)));
    }

    #[test]
    fn exec_args_run_inside_workspace_container() {
        let mut request = WorkspaceExecRequest {
            program: "cargo".to_owned(),
            args: vec!["test".to_owned()],
            stdin: Vec::new(),
            env_delta: BTreeMap::new(),
            cwd_inside_container: DEFAULT_CONTAINER_WORKDIR.to_owned(),
        };
        request
            .env_delta
            .insert("RUST_LOG".to_owned(), "debug".to_owned());

        let args = exec_args("container-1", &request);

        assert_eq!(args[0], "exec");
        assert!(args.contains(&"--workdir".to_owned()));
        assert!(args.contains(&DEFAULT_CONTAINER_WORKDIR.to_owned()));
        assert!(args.contains(&"RUST_LOG=debug".to_owned()));
        assert!(args.contains(&"container-1".to_owned()));
        assert!(args.contains(&"cargo".to_owned()));
        assert!(args.contains(&"test".to_owned()));
    }

    #[test]
    fn provider_exec_delegates_to_runtime_with_lease_container_id() {
        let temp = TempDir::new().expect("temp dir");
        let mut provider = provider(temp.path());
        let lease = provider.prepare(request()).expect("prepare");
        let exec_request = WorkspaceExecRequest {
            program: "sh".to_owned(),
            args: vec!["-c".to_owned(), "pwd".to_owned()],
            stdin: Vec::new(),
            env_delta: BTreeMap::new(),
            cwd_inside_container: DEFAULT_CONTAINER_WORKDIR.to_owned(),
        };

        let output = WorkspaceProvider::exec(&mut provider, &lease, exec_request)
            .expect("exec in workspace");

        assert!(output.success());
        assert_eq!(provider.runtime().execs.borrow().len(), 1);
        assert_eq!(provider.runtime().execs.borrow()[0].0, lease.id);
        assert_eq!(
            provider.runtime().execs.borrow()[0].1.cwd_inside_container,
            DEFAULT_CONTAINER_WORKDIR
        );
    }

    #[test]
    fn credential_free_environment_is_allowlisted() {
        let env = credential_free_env();

        assert_eq!(env.get("HOME"), Some(&DEFAULT_HOME.to_owned()));
        assert_eq!(env.get("GIT_TERMINAL_PROMPT"), Some(&"0".to_owned()));
        assert_eq!(env.get("GIT_CONFIG_NOSYSTEM"), Some(&"1".to_owned()));
        assert!(!has_credential_env(&env));
        assert!(!env.contains_key("SSH_AUTH_SOCK"));
        assert!(!env.contains_key("GITHUB_TOKEN"));
        assert!(!env.contains_key("FORGEJO_TOKEN"));
        assert!(!env.values().any(|value| value.contains("/home/")));
        assert!(!env.values().any(|value| value.contains(".ssh")));
        assert!(!env.values().any(|value| value.contains(".netrc")));
    }

    #[test]
    fn prepare_refuses_to_assert_isolation_when_resource_bounds_are_missing() {
        let temp = TempDir::new().expect("temp dir");
        let mut config = WorkspaceConfig::new(temp.path(), "localhost/pump19-workspace:stable");
        config.resources.memory_bytes = 0;
        let mut provider =
            ContainerWorkspaceProvider::with_runtime(config, RecordingRuntime::default());

        let error = provider
            .prepare(request())
            .expect_err("weak config refused");

        assert!(matches!(error, CoreError::Workspace(message) if message.contains("memory limit")));
        assert!(provider.runtime().created.borrow().is_empty());
    }

    #[test]
    fn prepare_refuses_to_assert_isolation_when_runtime_creation_fails() {
        let temp = TempDir::new().expect("temp dir");
        let config = WorkspaceConfig::new(temp.path(), "localhost/pump19-workspace:stable");
        let runtime = RecordingRuntime {
            fail_create: Some("podman unavailable".to_owned()),
            ..RecordingRuntime::default()
        };
        let mut provider = ContainerWorkspaceProvider::with_runtime(config, runtime);

        let error = provider
            .prepare(request())
            .expect_err("runtime failure refused");

        assert!(
            matches!(error, CoreError::Workspace(message) if message.contains("podman unavailable"))
        );
        assert!(provider.runtime().created.borrow().is_empty());
        let entries = fs::read_dir(temp.path())
            .expect("read temp root")
            .collect::<Result<Vec<_>, _>>()
            .expect("read entries");
        assert!(entries.is_empty());
    }

    #[test]
    fn cleanup_removes_container_and_host_control_directory() {
        let temp = TempDir::new().expect("temp dir");
        let mut provider = provider(temp.path());
        let lease = provider.prepare(request()).expect("prepare");
        fs::write(lease.root.join("residue"), b"leftover").expect("write residue");

        provider.cleanup(&lease).expect("cleanup");

        assert!(!lease.root.exists());
        assert_eq!(provider.runtime().removed.borrow().as_slice(), &[lease.id]);
    }
}
