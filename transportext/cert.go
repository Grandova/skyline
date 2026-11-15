package transportext

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	v2tls "github.com/v2fly/v2ray-core/v5/transport/internet/tls"
	"golang.org/x/crypto/curve25519"
)

type cachedCertPair struct {
	caCertPEM   []byte
	caKeyPEM    []byte
	leafCertPEM []byte
	leafKeyPEM  []byte
}

var (
	certCacheMu sync.Mutex
	certCache   = make(map[string]*cachedCertPair)
)

// RealityConfig holds validated REALITY parameters for a node with tls=2.
type RealityConfig struct {
	ServerName string
	PrivateKey []byte
	PublicKey  []byte
	ShortIDs   map[string]struct{}
	DestPort   int
}

// ParseRealityKey decodes a base64url or standard base64 X25519 key (32 bytes).
func ParseRealityKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("empty reality key")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	for _, enc := range encodings {
		if b, err := enc.DecodeString(encoded); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid reality key length or base64 encoding")
}

// BuildRealityConfig validates and constructs a RealityConfig from node settings.
func BuildRealityConfig(serverName, privateKeyStr, publicKeyStr, shortIDStr string, destPort int) (*RealityConfig, error) {
	var priv, pub []byte
	var err error
	if strings.TrimSpace(privateKeyStr) != "" {
		priv, err = ParseRealityKey(privateKeyStr)
		if err != nil {
			return nil, fmt.Errorf("parse reality private_key: %w", err)
		}
		derivedPub, err := curve25519.X25519(priv, curve25519.Basepoint)
		if err != nil {
			return nil, fmt.Errorf("derive reality public_key: %w", err)
		}
		if strings.TrimSpace(publicKeyStr) != "" {
			configuredPub, err := ParseRealityKey(publicKeyStr)
			if err != nil {
				return nil, fmt.Errorf("parse reality public_key: %w", err)
			}
			if !hmac.Equal(derivedPub, configuredPub) {
				return nil, fmt.Errorf("reality public_key does not match private_key")
			}
		}
		pub = derivedPub
	} else {
		priv = make([]byte, 32)
		if _, err := rand.Read(priv); err != nil {
			return nil, err
		}
		pub, err = curve25519.X25519(priv, curve25519.Basepoint)
		if err != nil {
			return nil, err
		}
	}

	shortIDs := make(map[string]struct{})
	shortIDs[""] = struct{}{}
	for _, part := range strings.Split(shortIDStr, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if _, err := hex.DecodeString(part); err != nil {
			return nil, fmt.Errorf("invalid reality short_id %q: %w", part, err)
		}
		shortIDs[part] = struct{}{}
	}

	if destPort <= 0 {
		destPort = 443
	}
	if serverName == "" {
		serverName = "www.microsoft.com"
	}

	return &RealityConfig{
		ServerName: serverName,
		PrivateKey: priv,
		PublicKey:  pub,
		ShortIDs:   shortIDs,
		DestPort:   destPort,
	}, nil
}

// ComputeRealityAuthTag computes an HMAC-SHA512 authentication tag for REALITY session verification.
func (r *RealityConfig) ComputeRealityAuthTag(clientEphemeralPub []byte, shortIDHex string) ([]byte, error) {
	shared, err := curve25519.X25519(r.PrivateKey, clientEphemeralPub)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha512.New, shared)
	mac.Write([]byte("REALITY-MX"))
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(shortIDHex))))
	return mac.Sum(nil), nil
}

// BuildV2TLSConfig builds a v2ray-core tls.Config for either TLS (tls=1) or REALITY (tls=2).
// If certFile/keyFile or inline PEMs are provided, they are used; otherwise a self-signed CA + leaf
// certificate is generated and cached in memory for the target serverName/host.
func BuildV2TLSConfig(serverName, host, certFile, keyFile, certPEMStr, keyPEMStr string, allowInsecure bool) (*v2tls.Config, error) {
	domain := strings.TrimSpace(serverName)
	if domain == "" {
		domain = strings.TrimSpace(host)
	}
	if domain == "" {
		domain = "localhost"
	}

	var certBytes, keyBytes []byte
	var err error

	if strings.TrimSpace(certPEMStr) != "" && strings.TrimSpace(keyPEMStr) != "" {
		certBytes = []byte(certPEMStr)
		keyBytes = []byte(keyPEMStr)
	} else if strings.TrimSpace(certFile) != "" && strings.TrimSpace(keyFile) != "" {
		certBytes, err = os.ReadFile(certFile)
		if err != nil {
			return nil, fmt.Errorf("read cert_file %s: %w", certFile, err)
		}
		keyBytes, err = os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read key_file %s: %w", keyFile, err)
		}
	}

	var certs []*v2tls.Certificate
	if len(certBytes) > 0 && len(keyBytes) > 0 {
		if _, err := gotls.X509KeyPair(certBytes, keyBytes); err != nil {
			return nil, fmt.Errorf("invalid TLS X509 key pair: %w", err)
		}
		certs = append(certs, &v2tls.Certificate{
			Certificate: certBytes,
			Key:         keyBytes,
			Usage:       v2tls.Certificate_ENCIPHERMENT,
		})
	} else {
		pair, err := getOrCreateSelfSignedPair(domain, host)
		if err != nil {
			return nil, err
		}
		certs = append(certs,
			&v2tls.Certificate{
				Certificate: pair.leafCertPEM,
				Key:         pair.leafKeyPEM,
				Usage:       v2tls.Certificate_ENCIPHERMENT,
			},
			&v2tls.Certificate{
				Certificate: pair.caCertPEM,
				Key:         pair.caKeyPEM,
				Usage:       v2tls.Certificate_AUTHORITY_ISSUE,
			},
		)
	}

	return &v2tls.Config{
		AllowInsecure: allowInsecure,
		ServerName:    domain,
		Certificate:   certs,
		NextProtocol:  []string{"h2", "http/1.1"},
	}, nil
}

// CertificateFingerprintSHA256 returns the hex-encoded SHA-256 fingerprint of the first leaf certificate in cfg.
func CertificateFingerprintSHA256(cfg *v2tls.Config) string {
	if cfg == nil {
		return ""
	}
	for _, c := range cfg.Certificate {
		if c.Usage == v2tls.Certificate_ENCIPHERMENT {
			block, _ := pem.Decode(c.Certificate)
			if block != nil {
				sum := sha256.Sum256(block.Bytes)
				return hex.EncodeToString(sum[:])
			}
		}
	}
	return ""
}

func getOrCreateSelfSignedPair(domain, host string) (*cachedCertPair, error) {
	cacheKey := domain + "|" + host
	certCacheMu.Lock()
	defer certCacheMu.Unlock()

	if existing, ok := certCache[cacheKey]; ok {
		return existing, nil
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now().Add(-1 * time.Hour)
	caSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	caTmpl := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			CommonName:   "Skyline Root CA",
			Organization: []string{"Skyline MX"},
		},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, err
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leafSerial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	for _, candidate := range []string{domain, host} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if ip := net.ParseIP(candidate); ip != nil {
			ips = append(ips, ip)
		} else {
			dnsNames = append(dnsNames, candidate)
		}
	}

	leafTmpl := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			CommonName:   domain,
			Organization: []string{"Skyline MX"},
		},
		NotBefore:             now,
		NotAfter:              now.Add(5 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caTmpl, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, err
	}
	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	pair := &cachedCertPair{
		caCertPEM:   caCertPEM,
		caKeyPEM:    caKeyPEM,
		leafCertPEM: leafCertPEM,
		leafKeyPEM:  leafKeyPEM,
	}
	certCache[cacheKey] = pair
	return pair, nil
}
