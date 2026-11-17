// Demo code from pgmi. Do not copy this file into the project as is. It is a
// baseline to learn from, not a library. Read it, then
// write your own gateway that fits this project. Namespaces, modules and
// package names are examples: rename them to match the project and add its
// specifics freely.
//
//   DATABASE_URL=postgresql://gateway:secret@localhost:5432/mydb cargo run
//
// The environment variables are the table in ../../PROTOCOL.md
// ("Configuration"), the same in every reference gateway; src/lib.rs reads them.

use std::io::IsTerminal;

use pgmi_gateway::{Config, Gateway, router, serve};
use tokio::signal::unix::{SignalKind, signal};

#[tokio::main]
async fn main() -> Result<(), pgmi_gateway::BoxError> {
    // ADAPT: JSON lines, OpenTelemetry, your log levels.
    tracing_subscriber::fmt().with_target(false).with_ansi(std::io::stdout().is_terminal()).init();
    let config = Config::from_env()?;
    let address = format!("{}:{}", config.host, config.port);
    let interceptors = config.interceptors.join(", ");
    let gateway = Gateway::build(config)?;
    let listener = tokio::net::TcpListener::bind(&address).await?;
    tracing::info!(%address, interceptors, oidc = gateway.login.as_ref().and_then(|l| l.issuer()),
                   mcp_resource = gateway.resource.as_ref().map(|r| r.resource.as_str()), "listening");

    let mut terminate = signal(SignalKind::terminate())?;
    tokio::select! {
        () = serve(listener, router(gateway)) => {}
        _ = tokio::signal::ctrl_c() => {}
        _ = terminate.recv() => {}
    }
    Ok(())
}
