use aes::cipher::block_padding::Pkcs7;
use aes::cipher::{BlockModeDecrypt, KeyIvInit};
use base64ct::Encoding;
use md5::{Digest, Md5};
use rustls_pki_types::{PrivateKeyDer, PrivatePkcs1KeyDer, PrivatePkcs8KeyDer, PrivateSec1KeyDer};

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum KeyError {
    NeedsPassword,
    WrongPassword,
    Malformed(String),
}

const ENCRYPTED_PKCS8: &str = "-----BEGIN ENCRYPTED PRIVATE KEY-----";

pub(crate) fn is_encrypted(pem: &[u8]) -> bool {
    let text = String::from_utf8_lossy(pem);
    text.contains(ENCRYPTED_PKCS8) || text.contains("Proc-Type: 4,ENCRYPTED")
}

pub(crate) fn decrypt(
    pem: &[u8],
    password: Option<&str>,
) -> Result<PrivateKeyDer<'static>, KeyError> {
    let text = std::str::from_utf8(pem).map_err(|_| KeyError::Malformed("not PEM text".into()))?;
    let password = password.ok_or(KeyError::NeedsPassword)?;
    if text.contains(ENCRYPTED_PKCS8) {
        return pkcs8(text, password);
    }
    legacy(text, password)
}

struct Pem {
    label: String,
    dek: Option<String>,
    der: Vec<u8>,
}

fn pem(text: &str) -> Result<Pem, KeyError> {
    let malformed = |m: &str| KeyError::Malformed(m.to_string());
    let mut lines = text
        .lines()
        .map(str::trim)
        .skip_while(|l| !l.starts_with("-----BEGIN "));
    let label = lines
        .next()
        .and_then(|l| l.strip_prefix("-----BEGIN "))
        .and_then(|r| r.strip_suffix("-----"))
        .ok_or_else(|| malformed("no PEM block"))?
        .to_string();
    let mut dek = None;
    let mut body = String::new();
    for line in lines {
        if line.starts_with("-----END ") {
            break;
        }
        if let Some(v) = line.strip_prefix("DEK-Info:") {
            dek = Some(v.trim().to_string());
        } else if !line.contains(':') {
            body.push_str(line);
        }
    }
    let der = base64ct::Base64::decode_vec(&body).map_err(|_| malformed("bad base64"))?;
    Ok(Pem { label, dek, der })
}

fn pkcs8(text: &str, password: &str) -> Result<PrivateKeyDer<'static>, KeyError> {
    use pkcs8::der::Decode;
    let start = text.find(ENCRYPTED_PKCS8).unwrap_or(0);
    let block = pem(&text[start..])?;
    if let Ok(info) = pkcs12::pbe_params::EncryptedPrivateKeyInfo::from_der(&block.der)
        && info.encryption_algorithm.oid == pkcs12::PKCS_12_PBE_WITH_SHAAND3_KEY_TRIPLE_DES_CBC
    {
        return pbe_sha1_3des(&info, password);
    }
    let info = pkcs8::EncryptedPrivateKeyInfoRef::from_der(&block.der)
        .map_err(|e| KeyError::Malformed(e.to_string()))?;
    let doc = info
        .decrypt(password)
        .map_err(|_| KeyError::WrongPassword)?;
    let der = doc.as_bytes().to_vec();
    if !is_der_sequence(&der) {
        return Err(KeyError::WrongPassword);
    }
    Ok(PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(der)))
}

fn pbe_sha1_3des(
    info: &pkcs12::pbe_params::EncryptedPrivateKeyInfo,
    password: &str,
) -> Result<PrivateKeyDer<'static>, KeyError> {
    use pkcs12::kdf::{Pkcs12KeyType, derive_key_utf8};
    let params = info
        .encryption_algorithm
        .parameters
        .as_ref()
        .ok_or_else(|| KeyError::Malformed("PBE-SHA1-3DES without parameters".into()))?
        .decode_as::<pkcs12::pbe_params::Pkcs12PbeParams>()
        .map_err(|e| KeyError::Malformed(e.to_string()))?;
    if params.iterations < 1 {
        return Err(KeyError::Malformed(
            "PBE-SHA1-3DES iteration count is not positive".into(),
        ));
    }
    let salt = params.salt.as_bytes();
    let derive = |kind, len| {
        derive_key_utf8::<sha1::Sha1>(password, salt, kind, params.iterations, len)
            .map_err(|_| KeyError::WrongPassword)
    };
    let key = derive(Pkcs12KeyType::EncryptionKey, 24)?;
    let iv = derive(Pkcs12KeyType::Iv, 8)?;
    let mut data = info.encrypted_data.as_bytes().to_vec();
    let plain = run::<cbc::Decryptor<des::TdesEde3>>(&key, &iv, &mut data)
        .ok_or(KeyError::WrongPassword)?;
    if !is_der_sequence(&plain) {
        return Err(KeyError::WrongPassword);
    }
    Ok(PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(plain)))
}

fn legacy(text: &str, password: &str) -> Result<PrivateKeyDer<'static>, KeyError> {
    let malformed = |m: &str| KeyError::Malformed(m.to_string());
    let Pem {
        label,
        dek,
        der: mut data,
    } = pem(text)?;
    let dek = dek.ok_or_else(|| malformed("Proc-Type ENCRYPTED without DEK-Info"))?;
    let (cipher, iv_hex) = dek
        .split_once(',')
        .ok_or_else(|| malformed("DEK-Info without an IV"))?;
    let iv = hex(iv_hex.trim()).ok_or_else(|| malformed("DEK-Info IV is not hex"))?;
    let key_len = match cipher.trim() {
        "AES-128-CBC" => 16,
        "AES-192-CBC" | "DES-EDE3-CBC" => 24,
        "AES-256-CBC" => 32,
        other => return Err(KeyError::Malformed(format!("unsupported cipher {other}"))),
    };
    let iv_len = if cipher.trim() == "DES-EDE3-CBC" {
        8
    } else {
        16
    };
    if iv.len() != iv_len {
        return Err(malformed("DEK-Info IV has the wrong length"));
    }
    let key = bytes_to_key(password.as_bytes(), &iv[..8], key_len);
    let plain = match cipher.trim() {
        "AES-128-CBC" => run::<cbc::Decryptor<aes::Aes128>>(&key, &iv, &mut data),
        "AES-192-CBC" => run::<cbc::Decryptor<aes::Aes192>>(&key, &iv, &mut data),
        "AES-256-CBC" => run::<cbc::Decryptor<aes::Aes256>>(&key, &iv, &mut data),
        _ => run::<cbc::Decryptor<des::TdesEde3>>(&key, &iv, &mut data),
    }
    .ok_or(KeyError::WrongPassword)?;
    if !is_der_sequence(&plain) {
        return Err(KeyError::WrongPassword);
    }
    Ok(match label.as_str() {
        "RSA PRIVATE KEY" => PrivateKeyDer::Pkcs1(PrivatePkcs1KeyDer::from(plain)),
        "EC PRIVATE KEY" => PrivateKeyDer::Sec1(PrivateSec1KeyDer::from(plain)),
        _ => PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(plain)),
    })
}

fn run<C: KeyIvInit + BlockModeDecrypt>(key: &[u8], iv: &[u8], data: &mut [u8]) -> Option<Vec<u8>> {
    let d = C::new_from_slices(key, iv).ok()?;
    d.decrypt_padded::<Pkcs7>(data).ok().map(<[u8]>::to_vec)
}

fn bytes_to_key(password: &[u8], salt: &[u8], len: usize) -> Vec<u8> {
    let mut out = Vec::with_capacity(len + 16);
    let mut prev: Vec<u8> = Vec::new();
    while out.len() < len {
        let mut h = Md5::new();
        h.update(&prev);
        h.update(password);
        h.update(salt);
        prev = h.finalize().to_vec();
        out.extend_from_slice(&prev);
    }
    out.truncate(len);
    out
}

fn hex(s: &str) -> Option<Vec<u8>> {
    if !s.len().is_multiple_of(2) {
        return None;
    }
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(s.get(i..i + 2)?, 16).ok())
        .collect()
}

fn is_der_sequence(der: &[u8]) -> bool {
    let (Some(&0x30), Some(&first)) = (der.first(), der.get(1)) else {
        return false;
    };
    let (len, header) = match first {
        n if n < 0x80 => (usize::from(n), 2),
        0x81..=0x84 => {
            let k = usize::from(first & 0x7f);
            let Some(bytes) = der.get(2..2 + k) else {
                return false;
            };
            (
                bytes
                    .iter()
                    .fold(0usize, |acc, b| (acc << 8) | usize::from(*b)),
                2 + k,
            )
        }
        _ => return false,
    };
    header + len == der.len()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn evp_bytes_to_key_matches_openssl() {
        let key = bytes_to_key(b"password", &[1, 2, 3, 4, 5, 6, 7, 8], 32);
        assert_eq!(key.len(), 32);
        let mut h = Md5::new();
        h.update(b"password");
        h.update([1, 2, 3, 4, 5, 6, 7, 8]);
        assert_eq!(&key[..16], h.finalize().as_slice());
    }

    #[test]
    fn der_sequence_lengths() {
        assert!(is_der_sequence(&[0x30, 0x01, 0x00]));
        assert!(!is_der_sequence(&[0x30, 0x02, 0x00]));
        let mut long = vec![0x30, 0x82, 0x01, 0x00];
        long.resize(4 + 256, 0);
        assert!(is_der_sequence(&long));
    }

    #[test]
    fn pbe_sha1_3des_pkcs8_opens_like_openssl() {
        let encrypted = include_bytes!("cloud/testdata/client-ec-pbe-sha1-3des.pem");
        let plain = pem(include_str!("cloud/testdata/client-ec.pk8.pem"))
            .unwrap()
            .der;
        assert!(is_encrypted(encrypted));
        assert_eq!(
            decrypt(encrypted, Some("key-passphrase")),
            Ok(PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(plain)))
        );
        assert_eq!(
            decrypt(encrypted, Some("wrong")),
            Err(KeyError::WrongPassword)
        );
        assert_eq!(decrypt(encrypted, None), Err(KeyError::NeedsPassword));
    }

    #[test]
    fn needs_a_password() {
        let pem = b"-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-256-CBC,00000000000000000000000000000000\n\nAAAA\n-----END RSA PRIVATE KEY-----\n";
        assert!(is_encrypted(pem));
        assert_eq!(decrypt(pem, None), Err(KeyError::NeedsPassword));
        assert_eq!(decrypt(pem, Some("x")), Err(KeyError::WrongPassword));
    }
}
