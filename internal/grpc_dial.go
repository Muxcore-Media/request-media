package internal

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func meshInsecure() bool {
	return os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true"
}

func dialModuleGRPC(addr string) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption
	if meshInsecure() {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		certFile := os.Getenv("MUXCORE_TLS_CERT")
		keyFile := os.Getenv("MUXCORE_TLS_KEY")
		caFile := os.Getenv("MUXCORE_TLS_CA")
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("TLS required — set MUXCORE_TLS_CERT/MUXCORE_TLS_KEY or MUXCORE_INSECURE_DISABLE_TLS=true")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load client TLS cert/key: %w", err)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		if caFile != "" {
			pemBytes, err := os.ReadFile(caFile) //nolint:gosec // path from operator config
			if err != nil {
				return nil, fmt.Errorf("read TLS CA: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemBytes) {
				return nil, fmt.Errorf("parse TLS CA from %q", caFile)
			}
			tlsConfig.RootCAs = pool
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	}
	return grpc.NewClient(addr, opts...)
}
