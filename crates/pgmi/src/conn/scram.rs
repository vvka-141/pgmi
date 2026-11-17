//! The SCRAM-SHA-256 client (RFC 5802, RFC 7677) the wire tap runs for
//! pgmi: its iteration cap is Go's 10,000,000, which tokio-postgres's
//! 100,000 would refuse for a role an administrator hardened.

use base64ct::{Base64, Encoding};
use hmac::{Hmac, KeyInit, Mac};
use sha2::{Digest, Sha256};

pub(crate) const MAX_ITERATIONS: u32 = 10_000_000;

pub(crate) const SCRAM_SHA_256: &str = "SCRAM-SHA-256";
pub(crate) const SCRAM_SHA_256_PLUS: &str = "SCRAM-SHA-256-PLUS";

/// How the client binds the exchange to TLS: the GS2 header and the
/// channel-binding data that follow it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Binding {
    /// `n,,`: the client does not support channel binding.
    Unsupported,
    /// `y,,`: the client could bind but the server did not offer it.
    Unrequested,
    /// `p=tls-server-end-point,,` with the end-point hash.
    EndPoint(Vec<u8>),
}

impl Binding {
    fn gs2(&self) -> &'static str {
        match self {
            Binding::Unsupported => "n,,",
            Binding::Unrequested => "y,,",
            Binding::EndPoint(_) => "p=tls-server-end-point,,",
        }
    }

    fn cbind_input(&self) -> Vec<u8> {
        let mut v = self.gs2().as_bytes().to_vec();
        if let Binding::EndPoint(data) = self {
            v.extend_from_slice(data);
        }
        v
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum ScramError {
    Malformed(&'static str),
    Nonce,
    Iterations(u64),
    ServerSignature,
    ServerError(String),
}

impl std::fmt::Display for ScramError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ScramError::Malformed(what) => write!(f, "invalid SCRAM {what} from the server"),
            ScramError::Nonce => f.write_str("invalid SCRAM nonce from the server"),
            ScramError::Iterations(i) => write!(
                f,
                "the server asks for {i} SCRAM iterations; pgmi allows 1 to {MAX_ITERATIONS}"
            ),
            ScramError::ServerSignature => f.write_str(
                "invalid SCRAM server signature: the server did not prove it knows the password",
            ),
            ScramError::ServerError(e) => write!(f, "SCRAM error from the server: {e}"),
        }
    }
}

/// The client state after its first message.
#[derive(Debug, Clone)]
pub(crate) struct Client {
    password: Vec<u8>,
    binding: Binding,
    nonce: String,
    first_bare: String,
}

/// The server's first message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct ServerFirst {
    text: String,
    nonce: String,
    salt: Vec<u8>,
    iterations: u32,
}

/// The client's final message and the server signature it must get back.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Final {
    pub(crate) message: Vec<u8>,
    pub(crate) server_signature: Vec<u8>,
}

fn normalize(password: &[u8]) -> Vec<u8> {
    match std::str::from_utf8(password) {
        Ok(s) => match stringprep::saslprep(s) {
            Ok(p) => p.into_owned().into_bytes(),
            Err(_) => password.to_vec(),
        },
        Err(_) => password.to_vec(),
    }
}

impl Client {
    /// Starts an exchange with a fresh nonce.
    pub(crate) fn new(password: &[u8], binding: Binding) -> Self {
        let raw: [u8; 18] = rand::random();
        Self::with_nonce(password, binding, Base64::encode_string(&raw), "")
    }

    fn with_nonce(password: &[u8], binding: Binding, nonce: String, user: &str) -> Self {
        let first_bare = format!("n={user},r={nonce}");
        Client {
            password: normalize(password),
            binding,
            nonce,
            first_bare,
        }
    }

    /// The `client-first-message` that goes in `SASLInitialResponse`.
    pub(crate) fn first(&self) -> Vec<u8> {
        format!("{}{}", self.binding.gs2(), self.first_bare).into_bytes()
    }

    /// Checks the server's first message: its nonce extends ours and the
    /// iteration count is within `1..=MAX_ITERATIONS`.
    pub(crate) fn server_first(&self, data: &[u8]) -> Result<ServerFirst, ScramError> {
        let text = std::str::from_utf8(data).map_err(|_| ScramError::Malformed("message"))?;
        let mut parts = text.split(',');
        let nonce = parts
            .next()
            .and_then(|p| p.strip_prefix("r="))
            .ok_or(ScramError::Malformed("nonce"))?;
        let salt = parts
            .next()
            .and_then(|p| p.strip_prefix("s="))
            .ok_or(ScramError::Malformed("salt"))?;
        let iterations = parts
            .next()
            .and_then(|p| p.strip_prefix("i="))
            .ok_or(ScramError::Malformed("iteration count"))?;
        if !nonce.starts_with(&self.nonce) || nonce.len() <= self.nonce.len() {
            return Err(ScramError::Nonce);
        }
        let iterations: u64 = iterations
            .parse()
            .map_err(|_| ScramError::Malformed("iteration count"))?;
        if !(1..=u64::from(MAX_ITERATIONS)).contains(&iterations) {
            return Err(ScramError::Iterations(iterations));
        }
        Ok(ServerFirst {
            text: text.to_string(),
            nonce: nonce.to_string(),
            salt: Base64::decode_vec(salt).map_err(|_| ScramError::Malformed("salt"))?,
            iterations: u32::try_from(iterations)
                .map_err(|_| ScramError::Iterations(iterations))?,
        })
    }

    /// The `client-final-message`; the costly key derivation runs here, so
    /// callers run it off the async threads.
    pub(crate) fn finish(&self, server: &ServerFirst) -> Final {
        let mut salted = [0u8; 32];
        pbkdf2::pbkdf2_hmac::<Sha256>(&self.password, &server.salt, server.iterations, &mut salted);
        let client_key = hmac(&salted, b"Client Key");
        let stored_key = Sha256::digest(client_key);
        let without_proof = format!(
            "c={},r={}",
            Base64::encode_string(&self.binding.cbind_input()),
            server.nonce
        );
        let auth_message = format!("{},{},{}", self.first_bare, server.text, without_proof);
        let signature = hmac(&stored_key, auth_message.as_bytes());
        let proof: Vec<u8> = client_key
            .iter()
            .zip(signature.iter())
            .map(|(a, b)| a ^ b)
            .collect();
        let server_key = hmac(&salted, b"Server Key");
        Final {
            message: format!("{without_proof},p={}", Base64::encode_string(&proof)).into_bytes(),
            server_signature: hmac(&server_key, auth_message.as_bytes()).to_vec(),
        }
    }
}

fn hmac(key: &[u8], data: &[u8]) -> [u8; 32] {
    let Ok(mut mac) = <Hmac<Sha256> as KeyInit>::new_from_slice(key) else {
        return [0; 32];
    };
    mac.update(data);
    mac.finalize().into_bytes().into()
}

/// Checks the server's final message against the signature `finish`
/// computed.
pub(crate) fn verify(expected: &[u8], data: &[u8]) -> Result<(), ScramError> {
    let text = std::str::from_utf8(data).map_err(|_| ScramError::Malformed("final message"))?;
    if let Some(e) = text.strip_prefix("e=") {
        return Err(ScramError::ServerError(e.to_string()));
    }
    let v = text
        .split(',')
        .next()
        .and_then(|p| p.strip_prefix("v="))
        .ok_or(ScramError::Malformed("final message"))?;
    let got = Base64::decode_vec(v).map_err(|_| ScramError::Malformed("final message"))?;
    if got == expected {
        Ok(())
    } else {
        Err(ScramError::ServerSignature)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_rfc_7677_exchange_gives_its_proof_and_server_signature() {
        let c = Client::with_nonce(
            b"pencil",
            Binding::Unsupported,
            "rOprNGfwEbeRWgbNEkqO".into(),
            "user",
        );
        assert_eq!(c.first(), b"n,,n=user,r=rOprNGfwEbeRWgbNEkqO");
        let s = c
            .server_first(b"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")
            .unwrap();
        let f = c.finish(&s);
        assert_eq!(
            String::from_utf8(f.message).unwrap(),
            "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
        );
        verify(
            &f.server_signature,
            b"v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=",
        )
        .unwrap();
        assert_eq!(
            verify(&f.server_signature, b"v=AAAA"),
            Err(ScramError::ServerSignature)
        );
    }

    #[test]
    fn the_iteration_count_is_capped_at_go_s_ten_million_and_the_nonce_must_extend_ours() {
        let c = Client::with_nonce(b"pw", Binding::Unsupported, "abc".into(), "");
        let first = |i: &str| c.server_first(format!("r=abcXYZ,s=c2FsdA==,i={i}").as_bytes());
        assert_eq!(first("600000").unwrap().iterations, 600_000);
        assert_eq!(first("10000000").unwrap().iterations, 10_000_000);
        assert_eq!(first("300000000"), Err(ScramError::Iterations(300_000_000)));
        assert_eq!(first("0"), Err(ScramError::Iterations(0)));
        assert_eq!(
            c.server_first(b"r=zzzXYZ,s=c2FsdA==,i=4096"),
            Err(ScramError::Nonce)
        );
        assert_eq!(
            c.server_first(b"r=abc,s=c2FsdA==,i=4096"),
            Err(ScramError::Nonce)
        );
    }

    #[test]
    fn channel_binding_goes_into_the_header_and_the_c_attribute() {
        let c = Client::with_nonce(b"pw", Binding::EndPoint(vec![1, 2, 3]), "n".into(), "");
        assert!(c.first().starts_with(b"p=tls-server-end-point,,n=,r=n"));
        let s = c.server_first(b"r=nX,s=c2FsdA==,i=1").unwrap();
        let msg = String::from_utf8(c.finish(&s).message).unwrap();
        let mut want = b"p=tls-server-end-point,,".to_vec();
        want.extend([1, 2, 3]);
        assert!(msg.starts_with(&format!("c={},r=nX,p=", Base64::encode_string(&want))));
        let y = Client::with_nonce(b"pw", Binding::Unrequested, "n".into(), "");
        assert!(y.first().starts_with(b"y,,"));
    }
}
