package branding

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateLogoRecognizesAndNormalizesSupportedContent(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		declared    string
		content     []byte
		contentType string
		resultName  string
	}{
		{name: "png", filename: "Company Logo.any", declared: "image/png", content: append([]byte("\x89PNG\r\n\x1a\n"), []byte("test")...), contentType: "image/png", resultName: "Company-Logo.png"},
		{name: "jpeg", filename: "logo.jpeg", declared: "application/octet-stream", content: []byte("\xff\xd8\xfftest"), contentType: "image/jpeg", resultName: "logo.jpg"},
		{name: "gif", filename: "logo", content: []byte("GIF89atest"), contentType: "image/gif", resultName: "logo.gif"},
		{name: "webp", filename: "logo.webp", content: []byte("RIFF0000WEBPtest"), contentType: "image/webp", resultName: "logo.webp"},
		{name: "svg", filename: "logo.svg", declared: "image/svg+xml; charset=utf-8", content: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle cx="5" cy="5" r="5"/></svg>`), contentType: "image/svg+xml", resultName: "logo.svg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logo, err := ValidateLogo(test.filename, test.declared, test.content)
			if err != nil {
				t.Fatalf("ValidateLogo: %v", err)
			}
			if logo.ContentType != test.contentType || logo.Filename != test.resultName {
				t.Fatalf("logo = %#v", logo)
			}
		})
	}
}

func TestValidateLogoRejectsUnsafeSVGAndMismatchedContent(t *testing.T) {
	unsafe := []string{
		`<!DOCTYPE svg><svg/>`,
		`<svg><script>alert(1)</script></svg>`,
		`<svg onload="alert(1)"/>`,
		`<svg><use href="https://example.com/image.svg"/></svg>`,
		`<svg><path fill="url(https://example.com/a)"/></svg>`,
		`<svg><path style="background:javascript:alert(1)"/></svg>`,
	}
	for _, value := range unsafe {
		if _, err := ValidateLogo("logo.svg", "image/svg+xml", []byte(value)); !errors.Is(err, ErrInvalidLogo) {
			t.Fatalf("unsafe SVG error = %v for %s", err, value)
		}
	}
	if _, err := ValidateLogo("logo.png", "image/jpeg", []byte("\x89PNG\r\n\x1a\ntest")); !errors.Is(err, ErrInvalidLogo) {
		t.Fatalf("mismatched type error = %v", err)
	}
	if _, err := ValidateLogo("logo.png", "image/png", make([]byte, MaxLogoBytes+1)); !errors.Is(err, ErrInvalidLogo) {
		t.Fatalf("oversized logo error = %v", err)
	}
	if _, err := ValidateLogo(strings.Repeat("x", 129)+".png", "image/png", []byte("\x89PNG\r\n\x1a\ntest")); !errors.Is(err, ErrInvalidLogo) {
		t.Fatalf("overlong filename error = %v", err)
	}
}
