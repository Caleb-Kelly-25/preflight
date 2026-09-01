package main

import (
	"strings"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// The simulator rejects a context entry whose type does not match how the policy
// uses the key, so a mistyped value is not a cosmetic problem — it is a check
// that silently cannot run. These cases pin the parsing that decides the type.
func TestContextFlagParses(t *testing.T) {
	tests := map[string]struct {
		arg  string
		want finding.ContextEntry
	}{
		"bare key defaults to string": {
			arg:  "aws:PrincipalTag/team=platform",
			want: finding.ContextEntry{Key: "aws:PrincipalTag/team", Type: finding.ContextString, Values: []string{"platform"}},
		},
		"explicit ip": {
			arg:  "aws:SourceIp@ip=10.0.0.1",
			want: finding.ContextEntry{Key: "aws:SourceIp", Type: finding.ContextIP, Values: []string{"10.0.0.1"}},
		},
		"explicit boolean": {
			arg:  "aws:SecureTransport@boolean=true",
			want: finding.ContextEntry{Key: "aws:SecureTransport", Type: finding.ContextBoolean, Values: []string{"true"}},
		},
		"explicit arn": {
			arg:  "aws:PrincipalArn@arn=arn:aws:iam::123456789012:role/deploy",
			want: finding.ContextEntry{Key: "aws:PrincipalArn", Type: finding.ContextARN, Values: []string{"arn:aws:iam::123456789012:role/deploy"}},
		},
		"stringList splits on comma": {
			arg:  "aws:TagKeys@stringList=env,team,owner",
			want: finding.ContextEntry{Key: "aws:TagKeys", Type: finding.ContextStringList, Values: []string{"env", "team", "owner"}},
		},
		// An explicit string type is the escape hatch for a value that
		// legitimately contains a comma; without it the parser refuses rather
		// than guessing. See TestContextFlagRejects.
		"explicit string keeps commas": {
			arg:  "aws:UserAgent@string=Go-http-client/2.0, aws-sdk-go-v2",
			want: finding.ContextEntry{Key: "aws:UserAgent", Type: finding.ContextString, Values: []string{"Go-http-client/2.0, aws-sdk-go-v2"}},
		},
		// The value is split on the first "=" only, so an ARN or a base64 value
		// containing "=" survives intact.
		"value may contain equals": {
			arg:  "aws:PrincipalTag/token@string=abc==",
			want: finding.ContextEntry{Key: "aws:PrincipalTag/token", Type: finding.ContextString, Values: []string{"abc=="}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var c contextFlag
			if err := c.Set(tc.arg); err != nil {
				t.Fatalf("Set(%q) returned %v", tc.arg, err)
			}
			if len(c) != 1 {
				t.Fatalf("Set(%q) produced %d entries, want 1", tc.arg, len(c))
			}
			got := c[0]
			if got.Key != tc.want.Key {
				t.Errorf("key = %q, want %q", got.Key, tc.want.Key)
			}
			if got.Type != tc.want.Type {
				t.Errorf("type = %q, want %q", got.Type, tc.want.Type)
			}
			if strings.Join(got.Values, "\x00") != strings.Join(tc.want.Values, "\x00") {
				t.Errorf("values = %q, want %q", got.Values, tc.want.Values)
			}
		})
	}
}

func TestContextFlagRejects(t *testing.T) {
	tests := map[string]string{
		"no equals":        "aws:SourceIp",
		"empty key":        "=value",
		"empty key w/type": "@ip=10.0.0.1",
		"unknown type":     "aws:SourceIp@ipv4=10.0.0.1",
		// The old behaviour split any comma-bearing value into a list, which
		// both mangled legitimate values and hid the type inference. Ambiguity
		// is now the caller's to resolve rather than the parser's to guess.
		"ambiguous comma": "aws:UserAgent=Go-http-client/2.0, aws-sdk-go-v2",
	}

	for name, arg := range tests {
		t.Run(name, func(t *testing.T) {
			var c contextFlag
			if err := c.Set(arg); err == nil {
				t.Errorf("Set(%q) was accepted, want an error; got %+v", arg, c)
			}
		})
	}
}

func TestContextFlagAccumulates(t *testing.T) {
	var c contextFlag
	for _, arg := range []string{"aws:SourceIp@ip=10.0.0.1", "aws:SecureTransport@boolean=true"} {
		if err := c.Set(arg); err != nil {
			t.Fatalf("Set(%q) returned %v", arg, err)
		}
	}
	if len(c) != 2 {
		t.Fatalf("got %d entries, want 2", len(c))
	}
	if c[0].Key != "aws:SourceIp" || c[1].Key != "aws:SecureTransport" {
		t.Errorf("keys not preserved in order: %q, %q", c[0].Key, c[1].Key)
	}
}
