package service

import (
	"context"
	"net/url"
	"testing"
)

func TestStartKiroIDEAuthBuildsKiroIDEURL(t *testing.T) {
	svc := NewKiroOAuthService(nil)

	result, err := svc.StartKiroIDEAuth(
		context.Background(),
		"us-west-2",
		"https://example.awsapps.com/start/",
		"http://localhost:3128",
		"KiroIDE",
		nil,
	)
	if err != nil {
		t.Fatalf("StartKiroIDEAuth returned error: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("SessionID is empty")
	}
	if result.State == "" {
		t.Fatal("State is empty")
	}
	if result.CodeChallenge == "" {
		t.Fatal("CodeChallenge is empty")
	}
	if result.RedirectURI != "http://localhost:3128" {
		t.Fatalf("RedirectURI = %q", result.RedirectURI)
	}
	if result.StartURL != "https://example.awsapps.com/start" {
		t.Fatalf("StartURL = %q", result.StartURL)
	}

	parsed, err := url.Parse(result.AuthURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "app.kiro.dev" || parsed.Path != "/signin" {
		t.Fatalf("unexpected auth URL: %s", result.AuthURL)
	}
	q := parsed.Query()
	assertQueryValue(t, q, "state", result.State)
	assertQueryValue(t, q, "code_challenge", result.CodeChallenge)
	assertQueryValue(t, q, "code_challenge_method", "S256")
	assertQueryValue(t, q, "redirect_uri", "http://localhost:3128")
	assertQueryValue(t, q, "redirect_from", "KiroIDE")
	assertQueryValue(t, q, "start_url", "https://example.awsapps.com/start")
}

func TestNormalizeKiroStartURL(t *testing.T) {
	got, err := normalizeKiroStartURL("")
	if err != nil {
		t.Fatalf("normalize empty start URL: %v", err)
	}
	if got != kiroStartURL {
		t.Fatalf("default start URL = %q", got)
	}

	got, err = normalizeKiroStartURL("https://example.awsapps.com/start/#/ignored")
	if err != nil {
		t.Fatalf("normalize enterprise start URL: %v", err)
	}
	if got != "https://example.awsapps.com/start" {
		t.Fatalf("enterprise start URL = %q", got)
	}

	if _, err := normalizeKiroStartURL("http://example.awsapps.com/start"); err == nil {
		t.Fatal("expected http start URL to be rejected")
	}
}

func TestParseKiroIDECallback(t *testing.T) {
	code, state, err := parseKiroIDECallback("", "", "http://localhost:3128?code=abc%20123&state=state-1")
	if err != nil {
		t.Fatalf("parse callback URL: %v", err)
	}
	if code != "abc 123" || state != "state-1" {
		t.Fatalf("parsed callback = code %q state %q", code, state)
	}

	code, state, err = parseKiroIDECallback("plain-code", "state-2", "")
	if err != nil {
		t.Fatalf("parse explicit code/state: %v", err)
	}
	if code != "plain-code" || state != "state-2" {
		t.Fatalf("parsed explicit values = code %q state %q", code, state)
	}

	if _, _, err := parseKiroIDECallback("", "", "http://localhost:3128?state=missing-code"); err == nil {
		t.Fatal("expected missing code to fail")
	}
}

func assertQueryValue(t *testing.T, q url.Values, key, want string) {
	t.Helper()
	if got := q.Get(key); got != want {
		t.Fatalf("query %s = %q, want %q", key, got, want)
	}
}
