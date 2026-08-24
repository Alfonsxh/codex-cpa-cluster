package branding

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxLogoBytes = 2 * 1024 * 1024

var (
	ErrInvalidLogo = errors.New("invalid branding logo")
	supportedTypes = map[string]string{
		"image/png":     ".png",
		"image/jpeg":    ".jpg",
		"image/gif":     ".gif",
		"image/webp":    ".webp",
		"image/svg+xml": ".svg",
	}
	blockedSVGElements = map[string]struct{}{
		"animate": {}, "animatemotion": {}, "animatetransform": {}, "audio": {},
		"embed": {}, "feimage": {}, "foreignobject": {}, "iframe": {},
		"image": {}, "object": {}, "script": {}, "set": {}, "style": {}, "video": {},
	}
	unsafeStylePattern = regexp.MustCompile(`(?i)(?:url\s*\(|@import|expression\s*\(|javascript:)`)
	styleURLPattern    = regexp.MustCompile(`(?i)url\s*\(\s*['"]?([^)'"\s]+)`)
	unsafeStemPattern  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

type Logo struct {
	Filename    string
	ContentType string
	Content     []byte
}

func ValidateLogo(filename string, declaredContentType string, content []byte) (Logo, error) {
	name := filepath.Base(strings.TrimSpace(filename))
	if name == "" || name == "." || utf8.RuneCountInString(name) > 128 {
		return Logo{}, fmt.Errorf("%w: filename is invalid", ErrInvalidLogo)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return Logo{}, fmt.Errorf("%w: filename contains control characters", ErrInvalidLogo)
		}
	}
	if len(content) == 0 {
		return Logo{}, fmt.Errorf("%w: file is empty", ErrInvalidLogo)
	}
	if len(content) > MaxLogoBytes {
		return Logo{}, fmt.Errorf("%w: file exceeds 2 MiB", ErrInvalidLogo)
	}
	detected := sniffContentType(content)
	extension, supported := supportedTypes[detected]
	if !supported {
		return Logo{}, fmt.Errorf("%w: only PNG, JPEG, GIF, WebP, and SVG are supported", ErrInvalidLogo)
	}
	declared := strings.ToLower(strings.TrimSpace(strings.Split(declaredContentType, ";")[0]))
	if declared != "" && declared != detected && declared != "application/octet-stream" {
		return Logo{}, fmt.Errorf("%w: declared type does not match content", ErrInvalidLogo)
	}
	if detected == "image/svg+xml" {
		if err := validateSVG(content); err != nil {
			return Logo{}, err
		}
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	stem = strings.Trim(unsafeStemPattern.ReplaceAllString(stem, "-"), ".-")
	if stem == "" {
		stem = "logo"
	}
	if len(stem) > 96 {
		stem = stem[:96]
	}
	return Logo{
		Filename:    stem + extension,
		ContentType: detected,
		Content:     append([]byte(nil), content...),
	}, nil
}

func sniffContentType(content []byte) string {
	switch {
	case bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(content, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(content, []byte("GIF87a")), bytes.HasPrefix(content, []byte("GIF89a")):
		return "image/gif"
	case len(content) >= 12 && bytes.Equal(content[:4], []byte("RIFF")) && bytes.Equal(content[8:12], []byte("WEBP")):
		return "image/webp"
	}
	prefix := content
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}
	prefix = bytes.ToLower(bytes.TrimLeft(prefix, "\xef\xbb\xbf\x00\t\r\n "))
	if bytes.HasPrefix(prefix, []byte("<svg")) ||
		(bytes.HasPrefix(prefix, []byte("<?xml")) && bytes.Contains(prefix, []byte("<svg"))) {
		return "image/svg+xml"
	}
	return ""
}

func validateSVG(content []byte) error {
	lowered := bytes.ToLower(content)
	if bytes.Contains(lowered, []byte("<!doctype")) || bytes.Contains(lowered, []byte("<!entity")) {
		return fmt.Errorf("%w: SVG cannot contain DOCTYPE or entity declarations", ErrInvalidLogo)
	}
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	elementCount := 0
	rootSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: SVG is not valid XML: %v", ErrInvalidLogo, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(start.Name.Local)
		if !rootSeen {
			rootSeen = true
			if name != "svg" {
				return fmt.Errorf("%w: SVG root element must be svg", ErrInvalidLogo)
			}
		}
		elementCount++
		if elementCount > 5000 {
			return fmt.Errorf("%w: SVG contains too many elements", ErrInvalidLogo)
		}
		if _, blocked := blockedSVGElements[name]; blocked {
			return fmt.Errorf("%w: SVG element %s is not allowed", ErrInvalidLogo, name)
		}
		for _, attribute := range start.Attr {
			attributeName := strings.ToLower(attribute.Name.Local)
			value := strings.TrimSpace(attribute.Value)
			loweredValue := strings.ToLower(value)
			if strings.HasPrefix(attributeName, "on") {
				return fmt.Errorf("%w: SVG event attributes are not allowed", ErrInvalidLogo)
			}
			if (attributeName == "href" || attributeName == "src") && value != "" && !strings.HasPrefix(value, "#") {
				return fmt.Errorf("%w: SVG external resources are not allowed", ErrInvalidLogo)
			}
			if attributeName == "style" && unsafeStylePattern.MatchString(loweredValue) {
				return fmt.Errorf("%w: SVG styles cannot reference scripts or external resources", ErrInvalidLogo)
			}
			for _, match := range styleURLPattern.FindAllStringSubmatch(loweredValue, -1) {
				if len(match) > 1 && !strings.HasPrefix(match[1], "#") {
					return fmt.Errorf("%w: SVG attribute references an external resource", ErrInvalidLogo)
				}
			}
			if strings.Contains(loweredValue, "javascript:") || strings.Contains(loweredValue, "data:text/html") {
				return fmt.Errorf("%w: SVG contains an unsafe URL", ErrInvalidLogo)
			}
		}
	}
	if !rootSeen {
		return fmt.Errorf("%w: SVG document is empty", ErrInvalidLogo)
	}
	return nil
}
