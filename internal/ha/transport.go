package ha

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// The node link is a dedicated mutual-TLS listener on the LAN address, never
// the management API: no reverse proxy, login session or web firewall sits in
// between, so every HTTP answer on it comes from the peer FireGateway itself.
//
// Both nodes derive the same private CA from the shared token, then each
// issues itself a short-lived leaf naming its node ID. A node accepts only a
// certificate from that CA naming the configured peer, so the token never
// travels and no certificate files need managing.

const peerCAInfo = "firegateway/peer-ca/v1"

func peerCA(token, cluster string) (*x509.Certificate, ed25519.PrivateKey, error) {
	seed, err := hkdf.Key(sha256.New, []byte(token), []byte(cluster), peerCAInfo, ed25519.SeedSize)
	if err != nil {
		return nil, nil, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "FireGateway peer CA " + cluster},
		NotBefore:             time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

// peerTLS returns the server and client TLS configurations of this node.
func peerTLS(token, cluster, node, address, peer string) (server, client *tls.Config, err error) {
	ca, caKey, err := peerCA(token, cluster)
	if err != nil {
		return nil, nil, err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: node},
		DNSNames:     []string{node},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if ip, e := netip.ParseAddr(address); e == nil {
		tmpl.IPAddresses = []net.IP{ip.AsSlice()}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		return nil, nil, err
	}
	leaf := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	server = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{leaf},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || cs.PeerCertificates[0].VerifyHostname(peer) != nil {
				return fmt.Errorf("client certificate does not name peer node %s", peer)
			}
			return nil
		},
	}
	client = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{leaf}, RootCAs: pool, ServerName: peer}
	return server, client, nil
}

// peerFailure classifies a transport error. Unreachable, refused, reset and
// timed out connections may mean the peer is down; a completed exchange that
// rejects our credentials proves it is up but misconfigured.
func peerFailure(err error) error {
	var cert *tls.CertificateVerificationError
	var alert tls.AlertError
	var op *net.OpError
	switch {
	case errors.As(err, &cert):
		return &PeerFault{"peer certificate rejected; use the same shared secret and node IDs on both nodes"}
	case errors.As(err, &alert), errors.As(err, &op) && op.Op == "remote error":
		return &PeerFault{"peer rejected this node's certificate; use the same shared secret and node IDs on both nodes"}
	case errors.Is(err, context.Canceled):
		return err
	}
	return fmt.Errorf("peer connection failed: %w", err)
}

func peerEndpoint(address string, port int) string {
	return net.JoinHostPort(address, strconv.Itoa(port))
}
