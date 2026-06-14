package jitsi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qognio/qognical/internal/adapters"
)

func TestPublicMode(t *testing.T) {
	conf, _ := json.Marshal(Config{BaseURL: "https://meet.example.org"})
	p, err := Factory(nil, conf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.CreateMeeting(context.Background(), adapters.MeetingRequest{BookingID: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got.JoinURL != "https://meet.example.org/qognical-abc" {
		t.Errorf("url = %q", got.JoinURL)
	}
}

func TestJWTMode(t *testing.T) {
	conf, _ := json.Marshal(Config{
		BaseURL: "https://meet.example.org", AppID: "qognical", JWTSecret: "supersecret",
	})
	p, err := Factory(nil, conf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.CreateMeeting(context.Background(), adapters.MeetingRequest{
		BookingID: "abc", InviteeName: "Test", InviteeMail: "t@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(got.JoinURL, "?jwt=", 2)
	if len(parts) != 2 {
		t.Fatalf("missing jwt in %q", got.JoinURL)
	}
	tokParts := strings.Split(parts[1], ".")
	if len(tokParts) != 3 {
		t.Fatalf("jwt shape: %v", tokParts)
	}
	pb, err := base64.RawURLEncoding.DecodeString(tokParts[1])
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(pb, &payload)
	if payload["room"] != "qognical-abc" {
		t.Errorf("payload.room = %v", payload["room"])
	}
	if payload["iss"] != "qognical" {
		t.Errorf("payload.iss = %v", payload["iss"])
	}
}

func TestMissingBaseURL(t *testing.T) {
	conf, _ := json.Marshal(Config{})
	if _, err := Factory(nil, conf); err == nil {
		t.Error("expected error for missing base_url")
	}
}
