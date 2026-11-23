package main

import (
	"net/url"
	"testing"
)

func TestPostgresURLEncodesCredentials(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("POSTGRES_HOST", "127.0.0.1")
	t.Setenv("POSTGRES_PORT", "5433")
	t.Setenv("POSTGRES_DB", "payment_db")
	t.Setenv("POSTGRES_USER", "payment user")
	t.Setenv("POSTGRES_PASSWORD", "p@ss:/?#%word")

	databaseURL, err := postgresURL()
	if err != nil {
		t.Fatalf("postgresURL() error = %v", err)
	}
	parsedURL, err := url.Parse(databaseURL.String())
	if err != nil {
		t.Fatalf("parse generated database URL: %v", err)
	}
	username := parsedURL.User.Username()
	password, ok := parsedURL.User.Password()
	if username != "payment user" || !ok || password != "p@ss:/?#%word" {
		t.Fatalf("credentials did not round-trip: username=%q password-preserved=%t", username, ok && password == "p@ss:/?#%word")
	}
	if parsedURL.Host != "127.0.0.1:5433" || parsedURL.Path != "/payment_db" {
		t.Fatalf("unexpected database address: %s", parsedURL)
	}
}
