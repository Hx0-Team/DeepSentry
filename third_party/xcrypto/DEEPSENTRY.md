# Pinned SSH compatibility patch

Upstream golang.org/x/crypto v0.57.0; BSD license is preserved. Only ssh/common.go, cipher.go, mac.go and kex.go add AES-192/256-CBC and HMAC-MD5/MD5-96 to the explicit insecure algorithm registry. Explicit SHA1 GEX permits a 1024-bit group minimum; SHA256 GEX remains at 2048. They are not modern defaults and remain excluded in FIPS mode. Go standard AES/HMAC implementations and the existing upstream SSH CBC packet framing/MAC validation are reused. Do not update this fork implicitly; rebase the four-file patch and rerun SSH packet tests and end-to-end negotiation/transfer regressions when upgrading upstream.

Unused upstream test trees are omitted; SSH unit tests/fixtures are retained for backend QA. The source does not contain provider keys.
