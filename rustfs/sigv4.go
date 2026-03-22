package main

// SigV4 verification for incoming S3 requests.
//
// We verify that the client knows the secret key associated with their
// access key ID. We do NOT re-verify the SHA-256 body hash — we trust the
// X-Amz-Content-Sha256 header value. This is acceptable for a research lab
// gateway; add body re-verification if you need stricter integrity checks.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// VerifySigV4 verifies an AWS SigV4 Authorization header against secretKey.
// Returns nil on success, a descriptive error otherwise.
func VerifySigV4(r *http.Request, secretKey string) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return fmt.Errorf("unsupported auth scheme (expected AWS4-HMAC-SHA256)")
	}

	params := parseAuthParams(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "))
	credential := params["Credential"]
	signedHeadersList := params["SignedHeaders"]
	providedSig := params["Signature"]
	if credential == "" || signedHeadersList == "" || providedSig == "" {
		return fmt.Errorf("incomplete Authorization header")
	}

	// Credential = AKID/YYYYMMDD/region/service/aws4_request
	credParts := strings.SplitN(credential, "/", 6)
	if len(credParts) != 5 {
		return fmt.Errorf("invalid credential scope (got %d parts)", len(credParts))
	}
	dateStr, region, service := credParts[1], credParts[2], credParts[3]

	// ── Canonical request ──────────────────────────────────────────────────
	canonicalURI := r.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalQuery := buildCanonicalQuery(r.URL.RawQuery)

	signedHeaders := strings.Split(signedHeadersList, ";")
	canonicalHeaders := buildCanonicalHeaders(r, signedHeaders)

	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = "UNSIGNED-PAYLOAD"
	}

	canonicalRequest := strings.Join([]string{
		r.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeadersList,
		payloadHash,
	}, "\n")

	// ── String to sign ─────────────────────────────────────────────────────
	amzDate := r.Header.Get("X-Amz-Date")
	credScope := strings.Join([]string{dateStr, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credScope,
		hashSHA256(canonicalRequest),
	}, "\n")

	// ── Signing key ────────────────────────────────────────────────────────
	dateKey := hmacSHA256bytes([]byte("AWS4"+secretKey), dateStr)
	regionKey := hmacSHA256bytes(dateKey, region)
	serviceKey := hmacSHA256bytes(regionKey, service)
	signingKey := hmacSHA256bytes(serviceKey, "aws4_request")

	expectedSig := hex.EncodeToString(hmacSHA256bytes(signingKey, stringToSign))

	// Constant-time comparison prevents timing attacks.
	if !hmac.Equal([]byte(expectedSig), []byte(providedSig)) {
		return fmt.Errorf("signature mismatch")
	}

	// ── Timestamp check (±15 minutes) ─────────────────────────────────────
	t, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return fmt.Errorf("invalid X-Amz-Date %q: %w", amzDate, err)
	}
	skew := time.Since(t)
	if skew < 0 {
		skew = -skew
	}
	if skew > 15*time.Minute {
		return fmt.Errorf("request timestamp too skewed (%v)", skew)
	}

	return nil
}

// parseAuthParams splits "Key=Value, Key=Value, ..." into a map.
func parseAuthParams(s string) map[string]string {
	m := make(map[string]string)
	for _, part := range strings.Split(s, ", ") {
		part = strings.TrimSpace(part)
		if i := strings.IndexByte(part, '='); i >= 0 {
			m[part[:i]] = part[i+1:]
		}
	}
	return m
}

// buildCanonicalQuery re-encodes a raw query string per the SigV4 spec:
// percent-encode everything except unreserved characters (A-Z a-z 0-9 - _ . ~),
// sort by key then value.
func buildCanonicalQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, seg := range strings.Split(rawQuery, "&") {
		if seg == "" {
			continue
		}
		parts := strings.SplitN(seg, "=", 2)
		k := sigv4Encode(queryUnescape(parts[0]))
		v := ""
		if len(parts) == 2 {
			v = sigv4Encode(queryUnescape(parts[1]))
		}
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// queryUnescape decodes percent-encoding and treats '+' as space.
func queryUnescape(s string) string {
	decoded, err := url.QueryUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

// sigv4Encode percent-encodes a string per RFC 3986 / SigV4 rules.
// Only unreserved characters (A-Z a-z 0-9 - _ . ~) are left unencoded.
func sigv4Encode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~'
}

// buildCanonicalHeaders returns the canonical headers string for the given
// list of signed header names (all lowercase, already sorted by the client).
func buildCanonicalHeaders(r *http.Request, signedHeaders []string) string {
	var b strings.Builder
	for _, h := range signedHeaders {
		var val string
		if h == "host" {
			val = r.Host
			if val == "" {
				val = r.Header.Get("Host")
			}
		} else {
			// Collapse internal whitespace runs to a single space.
			raw := r.Header.Get(h)
			val = strings.Join(strings.Fields(raw), " ")
		}
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(val)
		b.WriteByte('\n')
	}
	return b.String()
}

// ── Crypto helpers ─────────────────────────────────────────────────────────

func hmacSHA256bytes(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hashSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ExtractAccessKeyID pulls the access key ID out of the Authorization header.
func ExtractAccessKeyID(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return "", fmt.Errorf("missing or unsupported Authorization header")
	}
	params := parseAuthParams(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "))
	cred := params["Credential"]
	if cred == "" {
		return "", fmt.Errorf("Credential missing from Authorization header")
	}
	parts := strings.SplitN(cred, "/", 2)
	return parts[0], nil
}
