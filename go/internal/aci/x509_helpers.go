package aci

import (
	"crypto/ed25519"
	"crypto/x509"
	"fmt"
)

// marshalPKCS8PrivateKey wraps x509.MarshalPKCS8PrivateKey.
func marshalPKCS8PrivateKey(priv ed25519.PrivateKey) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(priv)
}

// parsePKCS8PrivateKey wraps x509.ParsePKCS8PrivateKey.
func parsePKCS8PrivateKey(der []byte) (interface{}, error) {
	return x509.ParsePKCS8PrivateKey(der)
}

// marshalPKIXPublicKey wraps x509.MarshalPKIXPublicKey.
func marshalPKIXPublicKey(pub ed25519.PublicKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(pub)
}

// parsePKIXPublicKey wraps x509.ParsePKIXPublicKey.
func parsePKIXPublicKey(der []byte) (interface{}, error) {
	return x509.ParsePKIXPublicKey(der)
}

// sentinel to silence unused-import in some build configs
var _ = fmt.Errorf
