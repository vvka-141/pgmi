use std::path::PathBuf;
use std::time::Duration;

use crate::config::params::Param;
use crate::conn::cloud::{self, Provider};
use crate::conn::resolve::Flags;

/// What one deploy is asked to do, whoever asks: the command line or a
/// `serve` tool call.
#[derive(Debug, Default)]
pub(crate) struct Request {
    pub(crate) project: PathBuf,
    pub(crate) connection: Flags,
    pub(crate) maintenance_db: Option<String>,
    pub(crate) cloud: Vec<Provider>,
    pub(crate) cloud_request: cloud::Options,
    pub(crate) params: Vec<Param>,
    pub(crate) params_files: Vec<String>,
    pub(crate) timeout: Option<Duration>,
    pub(crate) overwrite: bool,
    pub(crate) force: bool,
    pub(crate) compat: Option<String>,
    pub(crate) json: bool,
}
