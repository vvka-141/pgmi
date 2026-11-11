use std::io;
use std::time::Duration;

use socket2::{SockRef, TcpKeepalive};
use tokio::net::TcpStream;

/// TCP keepalive probes: the first after `idle` without traffic, then every
/// `interval`, and the connection drops after `count` unanswered.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Keepalive {
    pub(crate) idle: Duration,
    pub(crate) interval: Duration,
    pub(crate) count: u32,
}

impl Default for Keepalive {
    fn default() -> Self {
        Keepalive {
            idle: Duration::from_secs(15),
            interval: Duration::from_secs(15),
            count: 9,
        }
    }
}

/// The options set on every TCP socket before the startup message. With
/// `strict` (the user set one of them) a socket that refuses an option fails
/// the attempt; Go's defaults are otherwise applied as far as the system
/// allows.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Tcp {
    pub(crate) keepalive: Option<Keepalive>,
    pub(crate) user_timeout: Option<Duration>,
    pub(crate) strict: bool,
}

impl Default for Tcp {
    fn default() -> Self {
        Tcp {
            keepalive: Some(Keepalive::default()),
            user_timeout: None,
            strict: false,
        }
    }
}

impl Tcp {
    pub(crate) fn apply(&self, stream: &TcpStream) -> io::Result<()> {
        let _ = stream.set_nodelay(true);
        match self.set(&SockRef::from(stream)) {
            Err(e) if self.strict => Err(io::Error::new(
                e.kind(),
                format!("could not set the TCP keepalive or user timeout options: {e}"),
            )),
            _ => Ok(()),
        }
    }

    fn set(&self, socket: &SockRef<'_>) -> io::Result<()> {
        match self.keepalive {
            None => socket.set_keepalive(false)?,
            Some(k) => socket.set_tcp_keepalive(&params(k))?,
        }
        #[cfg(any(target_os = "linux", target_os = "android"))]
        socket.set_tcp_user_timeout(self.user_timeout)?;
        Ok(())
    }
}

fn params(k: Keepalive) -> TcpKeepalive {
    let p = TcpKeepalive::new()
        .with_time(k.idle)
        .with_interval(k.interval);
    #[cfg(any(
        target_os = "linux",
        target_os = "android",
        target_os = "macos",
        target_os = "windows"
    ))]
    let p = p.with_retries(k.count);
    p
}

#[cfg(test)]
mod tests {
    use super::*;

    async fn pair() -> (TcpStream, tokio::net::TcpListener) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let stream = TcpStream::connect(listener.local_addr().unwrap())
            .await
            .unwrap();
        (stream, listener)
    }

    #[tokio::test]
    async fn keepalive_and_user_timeout_reach_the_socket() {
        let (stream, _l) = pair().await;
        let tcp = Tcp {
            keepalive: Some(Keepalive {
                idle: Duration::from_secs(31),
                interval: Duration::from_secs(7),
                count: 4,
            }),
            user_timeout: Some(Duration::from_millis(2500)),
            strict: true,
        };
        tcp.apply(&stream).unwrap();
        let s = SockRef::from(&stream);
        assert!(s.keepalive().unwrap());
        assert!(s.tcp_nodelay().unwrap());
        #[cfg(any(target_os = "linux", target_os = "macos"))]
        {
            assert_eq!(s.tcp_keepalive_time().unwrap(), Duration::from_secs(31));
            assert_eq!(s.tcp_keepalive_interval().unwrap(), Duration::from_secs(7));
            assert_eq!(s.tcp_keepalive_retries().unwrap(), 4);
        }
        #[cfg(target_os = "linux")]
        assert_eq!(
            s.tcp_user_timeout().unwrap(),
            Some(Duration::from_millis(2500))
        );
    }

    #[tokio::test]
    async fn keepalive_is_on_by_default_with_go_s_timings_and_off_on_request() {
        let (stream, _l) = pair().await;
        Tcp::default().apply(&stream).unwrap();
        let s = SockRef::from(&stream);
        assert!(s.keepalive().unwrap());
        #[cfg(any(target_os = "linux", target_os = "macos"))]
        {
            assert_eq!(s.tcp_keepalive_time().unwrap(), Duration::from_secs(15));
            assert_eq!(s.tcp_keepalive_interval().unwrap(), Duration::from_secs(15));
            assert_eq!(s.tcp_keepalive_retries().unwrap(), 9);
        }
        let off = Tcp {
            keepalive: None,
            ..Tcp::default()
        };
        off.apply(&stream).unwrap();
        assert!(!SockRef::from(&stream).keepalive().unwrap());
    }
}
