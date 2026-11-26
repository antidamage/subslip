package redact

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	in := `Get "http://h/rest/ping?u=alice&p=hunter2&t=abc&s=salt&apiKey=K1&c=x": dial tcp; Bearer tok123`
	out := String(in)
	for _, secret := range []string{"hunter2", "abc", "salt", "K1", "tok123"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%q leaked in %q", secret, out)
		}
	}
	if !strings.Contains(out, "u=alice") || !strings.Contains(out, "c=x") {
		t.Fatalf("non-secret parameters removed: %q", out)
	}
}

func TestQuery(t *testing.T) {
	out := Query("u=bob&p=enc%3A6869&id=42")
	if strings.Contains(out, "6869") || !strings.Contains(out, "id=42") {
		t.Fatalf("got %q", out)
	}
}
