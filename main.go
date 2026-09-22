// Command generate_keys creates an Ed25519 key pair (the key type PASETO v4
// public uses) and writes both halves, base64-encoded, to keys.txt.
//
// This is a standalone dev/test helper, not part of the gateway or an Auth
// Server — just a quick way to get a real key pair to plug into
// gateway.yaml (public key) and, later, an Auth Server (private key)
// without wiring up either project yet.
//
// Usage:
//
//	go run generate_keys.go
//	go run generate_keys.go -out mykeys.txt
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	outPath := flag.String("out", "keys.txt", "file to write the generated keys to")
	flag.Parse()

	if err := run(*outPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(outPath string) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519 key pair: %w", err)
	}

	publicB64 := base64.StdEncoding.EncodeToString(publicKey)
	privateB64 := base64.StdEncoding.EncodeToString(privateKey)

	content := fmt.Sprintf(
		"# Ed25519 key pair for PASETO v4 public\n"+
			"# generated: %s\n"+
			"#\n"+
			"# PUBLIC key sizes are 32 bytes decoded; PRIVATE key sizes are 64 bytes decoded.\n"+
			"# This is a dev/test key pair. Do not use it in production, and never commit\n"+
			"# the private key value to git.\n"+
			"#\n"+
			"# Goes on the Auth Server (used to SIGN tokens). Keep secret.\n"+
			"PASETO_PRIVATE_KEY=%s\n"+
			"\n"+
			"# Goes on the Gateway, in gateway.yaml (used to VERIFY tokens). Safe to share.\n"+
			"PASETO_PUBLIC_KEY=%s\n",
		time.Now().UTC().Format(time.RFC3339),
		privateB64,
		publicB64,
	)

	if err := os.WriteFile(outPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	fmt.Printf("wrote key pair to %s\n", outPath)
	fmt.Println("public key (safe to share):", publicB64)

	return nil
}
