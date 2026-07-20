package sip

import (
	"strings"
	"testing"
)

func TestParseDigestChallengeAndRFC2617Response(t *testing.T) {
	challenge, err := ParseDigestChallenge(`Digest realm="testrealm@host.com", qop="auth,auth-int", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", opaque="5ccc069c403ebaf9f0171e9517f40e41"`)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Realm != "testrealm@host.com" || challenge.Nonce == "" || len(challenge.QOP) != 2 {
		t.Fatalf("challenge = %#v", challenge)
	}
	authorization, err := BuildDigestAuthorization(challenge, DigestAuthorizationOptions{
		Method:     "GET",
		URI:        "/dir/index.html",
		Username:   "Mufasa",
		Password:   "Circle Of Life",
		NonceCount: 1,
		CNonce:     "0a4f113b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authorization, `response="6629fae49393a05397450978507c4ef1"`) {
		t.Fatalf("authorization = %s", authorization)
	}
}

func TestDigestSHA256AndSessionAlgorithms(t *testing.T) {
	for _, algorithm := range []string{"SHA-256", "MD5-sess", "SHA-256-sess"} {
		t.Run(algorithm, func(t *testing.T) {
			authorization, err := BuildDigestAuthorization(DigestChallenge{
				Realm:     "asterisk",
				Nonce:     "nonce",
				Algorithm: algorithm,
				QOP:       []string{"auth"},
			}, DigestAuthorizationOptions{
				Method:     "REGISTER",
				URI:        "sip:pbx.example",
				Username:   "6001",
				Password:   "secret",
				NonceCount: 1,
				CNonce:     "abcdef",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(authorization, "algorithm="+algorithm) || !strings.Contains(authorization, "qop=auth") {
				t.Fatalf("authorization = %s", authorization)
			}
		})
	}
}

func TestDigestRejectsUnsupportedQOP(t *testing.T) {
	_, err := BuildDigestAuthorization(DigestChallenge{
		Realm: "asterisk",
		Nonce: "nonce",
		QOP:   []string{"unknown"},
	}, DigestAuthorizationOptions{Method: "REGISTER", URI: "sip:pbx", Username: "6001"})
	if err == nil {
		t.Fatal("unsupported qop was accepted")
	}
}
