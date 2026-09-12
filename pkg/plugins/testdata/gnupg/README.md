# GnuPG fixtures

Public keys and detached signatures over `manifest.json`, produced by GnuPG 2.4
rather than go-crypto, for `signature_openpgp_test.go`. They pin interoperability
with the tool operators actually sign with, and cover what go-crypto refuses to
produce at all (SHA-1 signatures and SHA-1 self-signatures). The private keys
were generated in a throwaway `GNUPGHOME` and discarded.

`manifest.json` deliberately has no `bundleSha256`. These fixtures exercise
the signature layer alone (`openPGPVerifier.Verify`), which checks a signature
over the manifest's exact bytes without parsing them; requiring the digest and
matching it against `bundle.zip` is the plugin loaders' job, tested separately.

| File | Made with |
| --- | --- |
| `eddsa-key.asc` | `gpg --quick-generate-key "default key" default default never` (GnuPG 2.4's default: legacy EdDSA on Ed25519) |
| `manifest.json.eddsa.asc` | `gpg --local-user "default key" --armor --detach-sign manifest.json` (SHA-512) |
| `rsa3072-key.asc` | `gpg --quick-generate-key "rsa key" rsa3072 sign never` |
| `manifest.json.rsa3072.asc` | `gpg --local-user "rsa key" --armor --detach-sign manifest.json` (SHA-512) |
| `manifest.json.rsa3072-sha1.asc` | the same, with `--digest-algo SHA1` |
| `rsa3072-sha1-self-signature-key.asc` | `gpg --cert-digest-algo SHA1 --quick-generate-key "sha1 cert key" rsa3072 sign never` |
