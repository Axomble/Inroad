package redisconn

import "testing"

func TestParseBareHostPort(t *testing.T) {
	opt, err := Parse("localhost:6379")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.Addr != "localhost:6379" {
		t.Errorf("Addr = %q, want localhost:6379", opt.Addr)
	}
	if opt.Password != "" || opt.DB != 0 || opt.TLSConfig != nil {
		t.Errorf("bare host:port should carry no auth/db/tls, got %+v", opt)
	}
}

func TestParseURLWithAuthAndDB(t *testing.T) {
	opt, err := Parse("redis://user:s3cret@redis.example.com:6380/3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.Addr != "redis.example.com:6380" {
		t.Errorf("Addr = %q", opt.Addr)
	}
	if opt.Username != "user" || opt.Password != "s3cret" {
		t.Errorf("credentials not carried: user=%q pass=%q", opt.Username, opt.Password)
	}
	if opt.DB != 3 {
		t.Errorf("DB = %d, want 3", opt.DB)
	}
	if opt.TLSConfig != nil {
		t.Errorf("redis:// (not rediss://) must not enable TLS")
	}
}

func TestParseRedissEnablesTLS(t *testing.T) {
	opt, err := Parse("rediss://redis.example.com:6380")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.TLSConfig == nil {
		t.Fatal("rediss:// must produce a TLSConfig")
	}
	if opt.TLSConfig.ServerName != "redis.example.com" {
		t.Errorf("ServerName = %q, want redis.example.com", opt.TLSConfig.ServerName)
	}
}

func TestParseMalformedURLErrors(t *testing.T) {
	if err := Validate("redis://%zz"); err == nil {
		t.Fatal("expected an error for a malformed redis:// URL")
	}
	if err := Validate("amqp://localhost:5672"); err == nil {
		t.Fatal("expected an error for a non-redis scheme")
	}
}

func TestValidateBareAddrOK(t *testing.T) {
	if err := Validate("127.0.0.1:6379"); err != nil {
		t.Errorf("bare addr should validate, got %v", err)
	}
}

func TestRedactStripsCredentials(t *testing.T) {
	got := Redact("redis://user:s3cret@redis.example.com:6380/1")
	if got != "redis://redis.example.com:6380/1" {
		t.Errorf("Redact = %q, still leaks or mangles", got)
	}
	if got := Redact("localhost:6379"); got != "localhost:6379" {
		t.Errorf("Redact of a bare addr changed it: %q", got)
	}
}
