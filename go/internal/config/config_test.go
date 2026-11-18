package config

import "testing"

func TestPostgresURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	// characters that have to be escaped in a url
	t.Setenv("POSTGRES_PASSWORD", "x@y/z")
	t.Setenv("POSTGRES_HOST", "db")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://ace:x%40y%2Fz@db:5432/ace?sslmode=disable"
	if c.DatabaseURL != want {
		t.Fatalf("got %s want %s", c.DatabaseURL, want)
	}
}
