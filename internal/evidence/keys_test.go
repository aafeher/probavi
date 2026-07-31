package evidence

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// testSeed is a fixed 32-byte seed: deterministic signatures keep the
// golden files stable.
func testSeed() []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	return seed
}

func testSigner() *Signer { return NewSignerFromSeed(testSeed()) }

func writeKeyFile(t *testing.T, name string, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path
}

func TestLoadSigner(t *testing.T) {
	seedHex := hex.EncodeToString(testSeed()) + "\n"

	s, err := LoadSigner(writeKeyFile(t, "ed25519.key", seedHex, 0o600))
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	if s.KeyID() != testSigner().KeyID() {
		t.Errorf("KeyID = %q, want %q", s.KeyID(), testSigner().KeyID())
	}

	if _, err := LoadSigner(writeKeyFile(t, "open.key", seedHex, 0o644)); !errors.Is(err, ErrKeyPermissions) {
		t.Errorf("world-readable key: got %v, want ErrKeyPermissions", err)
	}
	if _, err := LoadSigner(writeKeyFile(t, "short.key", "abcd\n", 0o600)); !errors.Is(err, ErrKeyFormat) {
		t.Errorf("short key: got %v, want ErrKeyFormat", err)
	}
	if _, err := LoadSigner(writeKeyFile(t, "upper.key", "AB"+seedHex[2:], 0o600)); !errors.Is(err, ErrKeyFormat) {
		t.Errorf("uppercase key: got %v, want ErrKeyFormat", err)
	}
	if _, err := LoadSigner(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Error("missing key file: expected error")
	}
}

func TestLoadPublicKey(t *testing.T) {
	pubHex := hex.EncodeToString(testSigner().PublicKey()) + "\n"
	pub, err := LoadPublicKey(writeKeyFile(t, "ed25519.pub", pubHex, 0o644))
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}
	if PublicKeyID(pub) != testSigner().KeyID() {
		t.Errorf("PublicKeyID = %q, want %q", PublicKeyID(pub), testSigner().KeyID())
	}
	if _, err := LoadPublicKey(writeKeyFile(t, "bad.pub", "zz\n", 0o644)); !errors.Is(err, ErrKeyFormat) {
		t.Errorf("bad public key: got %v, want ErrKeyFormat", err)
	}
}

func TestPublicKeyIDShape(t *testing.T) {
	id := PublicKeyID(testSigner().PublicKey())
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Errorf("PublicKeyID = %q, want 16 lowercase hex chars", id)
	}
}

func TestGenerateKeyPair(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "ed25519.key")
	pub := priv + ".pub"

	keyID, err := GenerateKeyPair(priv, pub)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	signer, err := LoadSigner(priv)
	if err != nil {
		t.Fatalf("LoadSigner on generated key: %v", err)
	}
	if signer.KeyID() != keyID {
		t.Errorf("KeyID = %q, want %q", signer.KeyID(), keyID)
	}
	loaded, err := LoadPublicKey(pub)
	if err != nil {
		t.Fatalf("LoadPublicKey on generated key: %v", err)
	}
	if PublicKeyID(loaded) != keyID {
		t.Errorf("public key derives %q, want %q", PublicKeyID(loaded), keyID)
	}
	if info, err := os.Stat(pub); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("public key mode/err = %v/%v, want 0644", info.Mode().Perm(), err)
	}

	if _, err := GenerateKeyPair(priv, pub); err == nil {
		t.Error("GenerateKeyPair overwrote an existing key")
	}
}

func TestGenerateKeyPairAtomicity(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "ed25519.key")
	// Public key path in a nonexistent directory: generation must fail AND
	// must not leave the freshly created private key behind.
	pub := filepath.Join(dir, "no", "such", "dir", "ed25519.pub")

	if _, err := GenerateKeyPair(priv, pub); err == nil {
		t.Fatal("GenerateKeyPair: expected error for uncreatable public key path")
	}
	if _, err := os.Stat(priv); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private key left behind after failed generation: stat err = %v", err)
	}

	if _, err := GenerateKeyPair(filepath.Join(dir, "no", "such", "k"), pub); err == nil {
		t.Error("GenerateKeyPair: expected error for uncreatable private key path")
	}
}

func TestKeyring(t *testing.T) {
	kr := NewKeyring(testSigner().PublicKey())
	if _, ok := kr[testSigner().KeyID()]; !ok {
		t.Error("keyring does not resolve the signer's key_id")
	}
	if len(kr) != 1 {
		t.Errorf("keyring size = %d, want 1", len(kr))
	}
}
