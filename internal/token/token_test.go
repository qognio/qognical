package token

import (
	"strings"
	"testing"
	"time"

	"github.com/qognio/qognical/internal/crypto"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	k, _ := crypto.GenerateMasterKey()
	m, _ := crypto.NewMaster(k)
	return New(m)
}

func TestIssueAndVerify(t *testing.T) {
	s := newSvc(t)
	tok, err := s.Issue("bk_1", ActionCancel, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bid, act, err := s.Verify(tok.String, tok.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if bid != "bk_1" || act != ActionCancel {
		t.Errorf("got bid=%s act=%s", bid, act)
	}
}

func TestRejectsTamperedSignature(t *testing.T) {
	s := newSvc(t)
	tok, _ := s.Issue("bk_1", ActionCancel, time.Hour)
	// flip a byte in the signature half.
	parts := strings.SplitN(tok.String, ".", 2)
	tampered := parts[0] + "." + flipFirst(parts[1])
	if _, _, err := s.Verify(tampered, tok.Hash); err == nil {
		t.Fatal("tampered token must not verify")
	}
}

func TestRejectsExpired(t *testing.T) {
	s := newSvc(t)
	tok, _ := s.Issue("bk_1", ActionCancel, -time.Second)
	if _, _, err := s.Verify(tok.String, tok.Hash); err != ErrExpired {
		t.Errorf("got %v, want ErrExpired", err)
	}
}

// TOK-4: token with mismatched stored hash (i.e. rotated after use) rejected.
func TestSingleUseRotation(t *testing.T) {
	s := newSvc(t)
	tok1, _ := s.Issue("bk_1", ActionCancel, time.Hour)
	// imagine the booking was cancelled; the server rotated the hash to a new token.
	tok2, _ := s.Issue("bk_1", ActionCancel, time.Hour)
	// Old token now fails because expected hash is tok2.Hash.
	if _, _, err := s.Verify(tok1.String, tok2.Hash); err != ErrAlreadyUsed {
		t.Errorf("expected ErrAlreadyUsed, got %v", err)
	}
}

// Empty expectedHash skips the single-use check (used for early discovery
// endpoints where the booking exists but no token has been stored yet —
// shouldn't happen in production, but the API documents the semantics).
func TestEmptyExpectedHashSkipsCheck(t *testing.T) {
	s := newSvc(t)
	tok, _ := s.Issue("bk_1", ActionView, time.Hour)
	if _, _, err := s.Verify(tok.String, ""); err != nil {
		t.Fatal(err)
	}
}

func flipFirst(s string) string {
	if len(s) == 0 {
		return s
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}
