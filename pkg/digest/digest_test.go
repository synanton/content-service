package digest

import (
	"bytes"
	"strings"
	"testing"
)

func TestComputeIsStableAndPrefixed(t *testing.T) {
	first := Compute([]byte("hello content-service"))
	second := Compute([]byte("hello content-service"))
	if first != second {
		t.Fatal("digest must be deterministic")
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest %q must carry the sha256: prefix", first)
	}
	if len(first) != len("sha256:")+64 {
		t.Fatalf("digest %q must be a 64-char hex hash", first)
	}
}

func TestComputeDistinguishesInputs(t *testing.T) {
	if Compute([]byte("a")) == Compute([]byte("b")) {
		t.Fatal("distinct inputs must yield distinct digests")
	}
	if Compute([]byte("a")) == Compute([]byte("a ")) {
		t.Fatal("trailing whitespace must change the digest")
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	payload := []byte("verify me")
	if !Verify(payload, Compute(payload)) {
		t.Fatal("verify must accept the computed digest")
	}
	if Verify(payload, "sha256:0000000000000000000000000000000000000000000000000000000000000000") {
		t.Fatal("verify must reject a wrong digest")
	}
}

func TestComputeFromReaderMatchesCompute(t *testing.T) {
	payload := []byte("streamed payload bytes")
	fromReader, err := ComputeFromReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("compute from reader: %v", err)
	}
	if fromReader != Compute(payload) {
		t.Fatal("reader and slice computation must agree")
	}
}
