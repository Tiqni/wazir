package githubauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/config"
)

// testKeyPEM generates a throwaway RSA private key in PKCS#1 PEM form.
func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func appConfig(privateKey string) config.Config {
	return config.Config{GitHub: config.GitHubConfig{AppID: 1, InstallationID: 2, PrivateKey: privateKey}}
}

func TestNewBuildsAppAuth(t *testing.T) {
	a, err := New(context.Background(), appConfig(string(testKeyPEM(t))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.HTTPClient == nil {
		t.Error("HTTPClient must be non-nil")
	}
	if a.GitToken == nil {
		t.Error("GitToken must be non-nil")
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(context.Background(), appConfig("not-a-pem-key")); err == nil {
		t.Fatal("expected an error for an unparseable private key")
	}
}

func TestLoadPrivateKeyAutoDetect(t *testing.T) {
	pemBytes := testKeyPEM(t)
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"file path":  path,
		"raw PEM":    string(pemBytes),
		"base64 PEM": base64.StdEncoding.EncodeToString(pemBytes),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := loadPrivateKey(v)
			if err != nil {
				t.Fatalf("loadPrivateKey: %v", err)
			}
			if !bytes.Equal(got, pemBytes) {
				t.Errorf("got %d bytes, want the original PEM", len(got))
			}
		})
	}
}

func TestLoadPrivateKeyEmpty(t *testing.T) {
	if _, err := loadPrivateKey(""); err == nil {
		t.Fatal("expected an error for an empty private key")
	}
}
