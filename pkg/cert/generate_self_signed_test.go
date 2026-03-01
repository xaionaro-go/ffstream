package cert

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSelfSignedForServer(t *testing.T) {
	cert, err := GenerateSelfSignedForServer()
	require.NoError(t, err)
	require.NotEmpty(t, cert.Certificate, "should have at least one certificate")

	// Parse the leaf certificate
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)

	// Verify organization
	require.Len(t, leaf.Subject.Organization, 1)
	assert.Equal(t, "DX.center", leaf.Subject.Organization[0])

	// Verify DNS names
	assert.Contains(t, leaf.DNSNames, "wingout.dx.center")

	// Verify extended key usage includes ServerAuth
	assert.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth)

	// Verify validity period (~10 years from now)
	assert.True(t, leaf.NotBefore.Before(time.Now().Add(time.Minute)))
	assert.True(t, leaf.NotAfter.After(time.Now().Add(9*365*24*time.Hour)))

	// Verify usable in tls.Config
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	assert.NotNil(t, tlsCfg)
}
