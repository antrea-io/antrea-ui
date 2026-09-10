// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugins

import (
	"bytes"
	"crypto"
	"fmt"
	"os"
	"time"

	// The v2 API, not github.com/ProtonMail/go-crypto/openpgp: only v2 enforces the algorithm
	// policy in openPGPPolicy. v1 silently ignores every Reject* and MinRSABits field, so the
	// same Config passed to v1 would accept SHA-1 signatures and DSA or RSA-1024 keys.
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// openPGPSignatureFileName is the detached, ASCII-armored OpenPGP signature over manifest.json's
// exact bytes - a ConfigMap key alongside manifest.json/bundle.zip, or a file alongside them in a
// plugin directory. Dots are legal in a ConfigMap key (the apiserver's key regex is
// [-._a-zA-Z0-9]+), same as the two existing names.
const openPGPSignatureFileName = manifestFileName + ".asc"

// openPGPMinRSABits is the smallest RSA modulus accepted, for a signing key and for the primary
// key it belongs to: ~128-bit security, and GnuPG's own RSA default since 2.2.17.
const openPGPMinRSABits = 3072

// openPGPPolicyDescription is openPGPPolicy in words, for the startup error naming a key that
// doesn't satisfy it. Keep in sync with openPGPPolicy and with docs/plugins.md.
const openPGPPolicyDescription = "an EdDSA (Ed25519, Ed448), ECDSA (NIST P-256, P-384, P-521) or RSA (at least 3072 bits) signing key, " +
	"with self-signatures using SHA-256, SHA-384, SHA-512, SHA3-256 or SHA3-512"

// openPGPPolicy is the go-crypto configuration every OpenPGP verification runs with. go-crypto's
// defaults are an exclusion list of known-broken algorithms; this is an allowlist instead,
// expressed as the Reject* maps go-crypto takes, so an algorithm is refused unless it is named
// here:
//
//   - hashes: SHA-256 or stronger from SHA-2 and SHA-3. Applied both to the plugin's signature
//     (attacker-supplied: anyone who can write a plugin ConfigMap chooses it) and to the self- and
//     binding signatures inside the trusted key ring, so one rule covers the whole chain. SHA-224
//     is excluded as the weakest of what is left, SHA-1 and older as broken. Nothing narrower than
//     that: Ed448 requires a 512-bit hash, and P-384/P-521 signers conventionally use SHA-384 and
//     SHA-512.
//   - public-key algorithms: RSA, ECDSA and EdDSA (both the legacy and the RFC 9580 encodings).
//     DSA and ElGamal are refused; the encryption-only algorithms can't sign anyway.
//   - RSA keys of at least openPGPMinRSABits.
//   - elliptic curves: the NIST curves and Curve25519/Curve448. go-crypto takes a curve exclusion
//     list, not an allowlist, so every other curve it knows is listed; a curve it doesn't know
//     fails to parse at all.
var openPGPPolicy = newOpenPGPPolicy()

func newOpenPGPPolicy() *packet.Config {
	allowedHashes := map[crypto.Hash]bool{
		crypto.SHA256:   true,
		crypto.SHA384:   true,
		crypto.SHA512:   true,
		crypto.SHA3_256: true,
		crypto.SHA3_512: true,
	}
	rejectedHashes := make(map[crypto.Hash]bool)
	for h := crypto.MD4; h <= crypto.BLAKE2b_512; h++ {
		if !allowedHashes[h] {
			rejectedHashes[h] = true
		}
	}

	allowedPublicKeyAlgorithms := map[packet.PublicKeyAlgorithm]bool{
		packet.PubKeyAlgoRSA:     true,
		packet.PubKeyAlgoECDSA:   true,
		packet.PubKeyAlgoEdDSA:   true,
		packet.PubKeyAlgoEd25519: true,
		packet.PubKeyAlgoEd448:   true,
	}
	rejectedPublicKeyAlgorithms := make(map[packet.PublicKeyAlgorithm]bool)
	for i := 0; i < 256; i++ {
		if algorithm := packet.PublicKeyAlgorithm(i); !allowedPublicKeyAlgorithms[algorithm] {
			rejectedPublicKeyAlgorithms[algorithm] = true
		}
	}

	return &packet.Config{
		MinRSABits:                  openPGPMinRSABits,
		RejectHashAlgorithms:        rejectedHashes,
		RejectMessageHashAlgorithms: rejectedHashes,
		RejectPublicKeyAlgorithms:   rejectedPublicKeyAlgorithms,
		RejectCurves: map[packet.Curve]bool{
			packet.CurveSecP256k1:     true,
			packet.CurveBrainpoolP256: true,
			packet.CurveBrainpoolP384: true,
			packet.CurveBrainpoolP512: true,
		},
	}
}

// openPGPVerifier is the SignatureVerifier for one OpenPGP trusted key file.
type openPGPVerifier struct {
	name    string
	keyring openpgp.EntityList
}

func (v *openPGPVerifier) Name() string {
	return v.name
}

func (v *openPGPVerifier) SignatureFileName() string {
	return openPGPSignatureFileName
}

// Verify checks signature, an armored detached OpenPGP signature, against the exact manifest
// bytes, using any key in the ring, under openPGPPolicy. The signing key must be valid now -
// neither expired nor revoked - where only the revocation signatures embedded in the key ring
// itself are honored, there is no keyserver lookup.
func (v *openPGPVerifier) Verify(manifest, signature []byte) error {
	sig, signer, err := openpgp.VerifyArmoredDetachedSignature(v.keyring, bytes.NewReader(manifest), bytes.NewReader(signature), openPGPPolicy)
	if err != nil {
		return err
	}
	// go-crypto checks the signing key's validity at the signature's creation time, not now:
	// the OpenPGP reading of expiry, where a key's expiry stops it making new signatures but
	// leaves the ones it already made valid. That creation time is whatever the signer writes
	// into the signature, though, so anyone holding an expired key could backdate a signature to
	// within its lifetime. Expiry is only a useful way to retire a trusted key if it stops the
	// key being trusted, so the key must also be valid at verification time.
	if _, ok := signer.SigningKeyById(openPGPPolicy.Now(), *sig.IssuerKeyId, openPGPPolicy); !ok {
		return fmt.Errorf("signing key %016X is expired or revoked", *sig.IssuerKeyId)
	}
	return nil
}

// armorBlockStart begins every ASCII-armor header line ("-----BEGIN PGP PUBLIC KEY BLOCK-----"
// and the like).
var armorBlockStart = []byte("-----BEGIN PGP ")

// loadOpenPGPKeyRing reads an OpenPGP public key file, ASCII-armored (the usual `gpg --armor
// --export` output) or binary. The file may hold more than one key, so a deployment can rotate
// signing keys by adding the new one before retiring the old - either as one armored block
// holding several keys (`gpg --armor --export old new`) or as several armored blocks one after
// the other (`cat new.asc >> public-key.asc`).
//
// Loaded once at startup, where a bad key file must fail the process rather than silently
// misconfigure verification. For the same reason a file that parses but yields no public key is
// an error here, and so is one holding a key that openPGPPolicy would refuse (see
// checkOpenPGPKeyPolicy): either would leave the operator believing a signer is trusted when
// every one of its signatures is going to be rejected. A file holding the same key twice is
// rejected too (see checkOpenPGPKeyIDsUnique).
func loadOpenPGPKeyRing(path string) (openpgp.EntityList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keyring openpgp.EntityList
	if starts := armorBlockStarts(data); len(starts) > 0 {
		keyring, err = readArmoredKeyRings(data, starts)
	} else {
		keyring, err = openpgp.ReadKeyRing(bytes.NewReader(data))
	}
	if err != nil {
		return nil, fmt.Errorf("not a valid OpenPGP public key file: %w", err)
	}
	if len(keyring) == 0 {
		return nil, fmt.Errorf("no OpenPGP public key found in %s", path)
	}
	if err := checkOpenPGPKeyIDsUnique(keyring); err != nil {
		return nil, err
	}
	for _, entity := range keyring {
		if err := checkOpenPGPKeyPolicy(entity); err != nil {
			return nil, err
		}
	}
	return keyring, nil
}

// checkOpenPGPKeyPolicy returns an error unless entity can sign under openPGPPolicy: a primary key
// with a self-signature using an allowed hash, and a signing key (the primary or a subkey) of an
// allowed algorithm and size. That is exactly the key selection go-crypto runs when it verifies a
// signature, so a key passing here is one whose signatures can verify; only time is taken out of
// it. An expired key is accepted, because its signatures are rejected at verification time
// anyway, and one lingering in the file after a rotation is not a misconfiguration worth
// refusing to start over. A revoked key is accepted for the same reason: carrying a key's
// revocation is a legitimate reason for it to be in the file.
func checkOpenPGPKeyPolicy(entity *openpgp.Entity) error {
	if entity.Revoked(time.Now()) {
		return nil
	}
	// The zero time disables every expiry check in go-crypto's key selection.
	if _, ok := entity.SigningKey(time.Time{}, openPGPPolicy); !ok {
		return fmt.Errorf("OpenPGP key %X can't sign under the plugin signature policy, which requires %s", entity.PrimaryKey.Fingerprint, openPGPPolicyDescription)
	}
	return nil
}

// checkOpenPGPKeyIDsUnique returns an error if any key ID, of a primary key or a subkey, belongs
// to more than one entity in keyring. go-crypto does not merge entities: reading a key exported
// twice yields two independent copies, and verification looks the signer up by key ID and uses
// only the first match. Every later copy is then dead weight, including whatever it carries that
// the first one doesn't - a revocation, an extended expiry. The natural way to end up here is
// appending a fresh export of a key already in the file (`cat revoked.asc >> public-key.asc`), and
// quietly keeping a revoked key trusted is the worst outcome of that, so it fails startup rather
// than being resolved either way. The key ID, not the fingerprint, because it is what the lookup
// matches on: two different keys colliding on it would shadow each other the same way.
func checkOpenPGPKeyIDsUnique(keyring openpgp.EntityList) error {
	owners := make(map[uint64]*openpgp.Entity)
	for _, entity := range keyring {
		ids := []uint64{entity.PrimaryKey.KeyId}
		for _, subkey := range entity.Subkeys {
			ids = append(ids, subkey.PublicKey.KeyId)
		}
		for _, id := range ids {
			if owner, ok := owners[id]; ok && owner != entity {
				return fmt.Errorf("OpenPGP key ID %016X appears in more than one key in the file (primary keys %X and %X): only the first would ever be used, so replace the key with a single, current export rather than appending another one", id, owner.PrimaryKey.Fingerprint, entity.PrimaryKey.Fingerprint)
			}
			owners[id] = entity
		}
	}
	return nil
}

// armorBlockStarts returns the offset of every armor header in data. Matched anywhere, not only
// at the start of a line: an export that doesn't end in a newline, with another appended after
// it, puts the second header right after the first block's END line.
func armorBlockStarts(data []byte) []int {
	var starts []int
	for offset := 0; ; {
		i := bytes.Index(data[offset:], armorBlockStart)
		if i < 0 {
			return starts
		}
		starts = append(starts, offset+i)
		offset += i + len(armorBlockStart)
	}
}

// readArmoredKeyRings reads every armored block in data, each starting at one of starts, into a
// single ring. openpgp.ReadArmoredKeyRing on its own stops after the first block - armor.Decode
// decodes one block and leaves its reader unusable - so a key file built by concatenating
// exports would otherwise load only its first key, silently. Each block is therefore decoded
// from its own slice of data. A block that fails to parse or holds no key fails the whole file
// rather than being skipped, for the same reason loadOpenPGPKeyRing rejects an empty ring.
func readArmoredKeyRings(data []byte, starts []int) (openpgp.EntityList, error) {
	var keyring openpgp.EntityList
	for i, start := range starts {
		end := len(data)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		entities, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(data[start:end]))
		if err != nil {
			return nil, fmt.Errorf("armored block %d: %w", i+1, err)
		}
		// ReadArmoredKeyRing returns an empty list, not an error, for a block holding no key.
		if len(entities) == 0 {
			return nil, fmt.Errorf("armored block %d holds no OpenPGP public key", i+1)
		}
		keyring = append(keyring, entities...)
	}
	return keyring, nil
}
