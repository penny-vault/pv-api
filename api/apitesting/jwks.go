// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package apitesting provides test-only helpers for exercising the api
// package's authentication stack: an RS256 keypair generated at BeforeSuite
// time and an in-process JWKS HTTP server the middleware can talk to.
package apitesting

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	// Issuer is the iss claim the suite's JWKS represents.
	Issuer = "https://test.pvapi.local/"

	// Audience is the aud claim valid tokens must carry.
	Audience = "https://api.pvapi.local"
)

// JWKS is the test harness: a fresh RSA keypair, the matching jwk.Set
// served over HTTP, and a Mint method to produce signed tokens.
type JWKS struct {
	Server *httptest.Server
	URL    string

	priv jwk.Key
	set  jwk.Set
}

// NewJWKS boots a fresh JWKS harness. Call Close when done.
func NewJWKS() (*JWKS, error) {
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating RSA key: %w", err)
	}

	priv, err := jwk.Import(pk)
	if err != nil {
		return nil, fmt.Errorf("importing private key: %w", err)
	}
	if err := priv.Set(jwk.KeyIDKey, "test-key-1"); err != nil {
		return nil, fmt.Errorf("setting kid: %w", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
		return nil, fmt.Errorf("setting alg: %w", err)
	}

	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		return nil, fmt.Errorf("deriving public key: %w", err)
	}

	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		return nil, fmt.Errorf("adding key to set: %w", err)
	}

	body, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("marshaling set: %w", err)
	}

	server := httptest.NewServer(jwksHandler(body))

	return &JWKS{
		Server: server,
		URL:    server.URL,
		priv:   priv,
		set:    set,
	}, nil
}

// Close shuts the JWKS HTTP server down.
func (j *JWKS) Close() {
	j.Server.Close()
}

// Mint produces an RS256 JWT with the given subject, using the package
// default audience and issuer. ttl is how long from now the token should
// remain valid.
func (j *JWKS) Mint(subject string, ttl time.Duration) (string, error) {
	return j.MintWith(subject, Audience, Issuer, ttl)
}

// MintWith lets tests override audience / issuer to exercise failure cases.
// A negative ttl produces an already-expired token.
func (j *JWKS) MintWith(subject, audience, issuer string, ttl time.Duration) (string, error) {
	tok, err := jwt.NewBuilder().
		Issuer(issuer).
		Audience([]string{audience}).
		Subject(subject).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(ttl)).
		Build()
	if err != nil {
		return "", fmt.Errorf("building token: %w", err)
	}

	payload, err := jwt.NewSerializer().Sign(jwt.WithKey(jwa.RS256(), j.priv)).Serialize(tok)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return string(payload), nil
}

// SignRaw signs an arbitrary payload with the suite's key. Used only to
// test cases where the payload is not a valid JWT claim set.
func (j *JWKS) SignRaw(payload []byte) ([]byte, error) {
	return jws.Sign(payload, jws.WithKey(jwa.RS256(), j.priv))
}

func jwksHandler(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	}
}
