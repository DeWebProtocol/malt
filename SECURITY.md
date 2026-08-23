# Security Policy

The MALT local runtime is experimental and is not production-ready. Security fixes may
make breaking changes before v1.

Do not open a public issue for a suspected vulnerability. Use GitHub private
vulnerability reporting for this repository or email security@deweb.world with
`SECURITY` in the subject or opening line. Include reproduction steps, the
affected commit, and any proof-of-concept data needed to reproduce the issue.

High-value reports include proof/result binding failures, payload bytes accepted
without CID verification, implicit trust of a gateway-generated root, unsafe
local file traversal, Unix-socket permission issues, and trusted-root state
corruption. Merkle DAG import reports should also cover malformed dag-pb
construction, unsafe local traversal, ignore-policy bypass, or a gateway CAS
response accepted without binding it to the uploaded block bytes.

Encrypted-backup reports should include decryption before root/ProofList/CID
verification, nonce reuse, keyring permission or replacement failures,
plaintext path/fingerprint disclosure to the Gateway, opaque-token derivation
or enumeration failures, unsafe materialization, symlink traversal, or
automatic trust of a pushed Bucket head. Every encrypted filesystem manifest
and file chunk uses XChaCha20-Poly1305, but AEAD does not replace locally
verifying the selected-root ProofList and ciphertext CID before decryption.
Backup publication also computes Map/List Roots locally and rejects any remote
CID or Root substitution. Unchanged remote bindings are never imported from an
observed-only base, and local snapshot/restore traversal stays within pinned
filesystem roots.

Configurable local state and key paths must be placed in a dedicated
owner-only directory. MALT applies owner-only file protection, including a
protected Windows DACL, but cannot prevent replacement by another principal
that already controls the parent directory.
