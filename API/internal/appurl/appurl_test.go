package appurl_test

import (
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/appurl"
	"github.com/stretchr/testify/assert"
)

func TestDerive(t *testing.T) {
	t.Parallel()

	longLabel := strings.Repeat("a", 64)
	maxLabel := strings.Repeat("a", 63)
	// 4 labels of 63 plus 3 dots is 255 bytes, over the 253 limit.
	longHost := strings.Join([]string{maxLabel, maxLabel, maxLabel, maxLabel}, ".")

	tests := []struct {
		name   string
		values string
		want   appurl.Access
	}{
		{"https by default", `{"ingress":{"host":"chat.example.nl"}}`, web("https://chat.example.nl")},
		{"tls true", `{"ingress":{"host":"chat.example.nl","tls":true}}`, web("https://chat.example.nl")},
		{"tls false uses http", `{"ingress":{"host":"chat.example.nl","tls":false}}`, web("http://chat.example.nl")},
		{"enabled true", `{"ingress":{"enabled":true,"host":"chat.example.nl"}}`, web("https://chat.example.nl")},
		{"host is lowercased and trimmed", `{"ingress":{"host":"  Chat.Example.NL "}}`, web("https://chat.example.nl")},
		{"63-byte label", `{"ingress":{"host":"` + maxLabel + `.example.nl"}}`, web("https://" + maxLabel + ".example.nl")},
		{"digits in labels", `{"ingress":{"host":"app-1.k8s2.example.nl"}}`, web("https://app-1.k8s2.example.nl")},

		{"empty values", ``, none()},
		{"invalid json", `{`, none()},
		{"no ingress", `{"replicas":2}`, none()},
		{"ingress null", `{"ingress":null}`, none()},
		{"ingress is a string", `{"ingress":"chat.example.nl"}`, none()},
		{"ingress is an array", `{"ingress":["chat.example.nl"]}`, none()},
		{"host is a number", `{"ingress":{"host":42}}`, none()},
		{"enabled false", `{"ingress":{"enabled":false,"host":"chat.example.nl"}}`, none()},
		{"empty host", `{"ingress":{"host":""}}`, none()},
		{"single label", `{"ingress":{"host":"chat"}}`, none()},
		{"trailing dot", `{"ingress":{"host":"chat.example.nl."}}`, none()},
		{"leading dot", `{"ingress":{"host":".example.nl"}}`, none()},
		{"empty label", `{"ingress":{"host":"chat..example.nl"}}`, none()},
		{"leading hyphen", `{"ingress":{"host":"-chat.example.nl"}}`, none()},
		{"trailing hyphen", `{"ingress":{"host":"chat-.example.nl"}}`, none()},
		{"64-byte label", `{"ingress":{"host":"` + longLabel + `.example.nl"}}`, none()},
		{"host over 253 bytes", `{"ingress":{"host":"` + longHost + `"}}`, none()},
		{"scheme in host", `{"ingress":{"host":"https://chat.example.nl"}}`, none()},
		{"javascript scheme", `{"ingress":{"host":"javascript:alert(1)"}}`, none()},
		{"port", `{"ingress":{"host":"chat.example.nl:8443"}}`, none()},
		{"path", `{"ingress":{"host":"chat.example.nl/admin"}}`, none()},
		{"query", `{"ingress":{"host":"chat.example.nl?x=1"}}`, none()},
		{"fragment", `{"ingress":{"host":"chat.example.nl#x"}}`, none()},
		{"userinfo", `{"ingress":{"host":"evil.com@chat.example.nl"}}`, none()},
		{"whitespace inside", `{"ingress":{"host":"chat example.nl"}}`, none()},
		{"underscore", `{"ingress":{"host":"chat_app.example.nl"}}`, none()},
		{"unicode lookalike", `{"ingress":{"host":"chаt.example.nl"}}`, none()},
		{"ipv4 literal", `{"ingress":{"host":"10.0.0.1"}}`, none()},
		{"short ipv4 form", `{"ingress":{"host":"127.1"}}`, none()},
		{"ipv6 literal", `{"ingress":{"host":"::1"}}`, none()},
		{"bracketed ipv6", `{"ingress":{"host":"[::1]"}}`, none()},
		{"numeric tld", `{"ingress":{"host":"chat.example.123"}}`, none()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, appurl.Derive([]byte(tt.values)))
		})
	}
}

func web(url string) appurl.Access { return appurl.Access{Kind: appurl.KindWeb, URL: url} }

func none() appurl.Access { return appurl.Access{Kind: appurl.KindNone} }
