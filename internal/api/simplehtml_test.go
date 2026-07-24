package api

import "strings"

import "testing"

// simpleHTML must HTML-escape untrusted title/body so a reflected query param
// (e.g. the OAuth callback's ?error) or an upstream error string cannot inject
// script into the host-facing page. Regression guard for the reflected-XSS fix.
func TestSimpleHTMLEscapesUntrustedText(t *testing.T) {
	out := simpleHTML("t<itle", `Microsoft meldete: <script>alert(1)</script>`)
	if strings.Contains(out, "<script>") {
		t.Fatalf("simpleHTML did not escape body — XSS vector present:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped <script> in output, got:\n%s", out)
	}
	if strings.Contains(out, "t<itle") {
		t.Fatal("simpleHTML did not escape title")
	}
}

// simpleHTMLPage is the trusted-markup path: the body is emitted verbatim (the
// approval confirm page relies on this for its <form>), but the title is still
// escaped.
func TestSimpleHTMLPageKeepsRawBodyEscapesTitle(t *testing.T) {
	out := simpleHTMLPage("v<erb", `<form method="POST"></form>`)
	if !strings.Contains(out, `<form method="POST">`) {
		t.Fatal("simpleHTMLPage must not escape trusted body markup")
	}
	if strings.Contains(out, "v<erb") {
		t.Fatal("simpleHTMLPage must still escape the title")
	}
}
