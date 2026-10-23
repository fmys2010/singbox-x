package v2rayxhttp

import (
	"crypto/rand"
	"math"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http2/hpack"
)

const (
	charsetBase62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// Huffman encoding gives ~20% size reduction for base62 sequences
	avgHuffmanBytesPerCharBase62 = 0.8
	validationTolerance          = 2
)

type paddingPlacement struct {
	placement string
	key       string
	header    string
	rawURL    string
}

type paddingConfig struct {
	length    int
	placement paddingPlacement
	method    string
}

func randStringFromCharset(n int, charset string) (string, bool) {
	if n <= 0 || len(charset) == 0 {
		return "", false
	}
	m := len(charset)
	limit := byte(256 - (256 % m))
	result := make([]byte, n)
	i := 0
	buffer := make([]byte, 256)
	for i < n {
		if _, err := rand.Read(buffer); err != nil {
			return "", false
		}
		for _, rb := range buffer {
			if rb >= limit {
				continue
			}
			result[i] = charset[int(rb)%m]
			i++
			if i == n {
				break
			}
		}
	}
	return string(result), true
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func generateTokenishPaddingBase62(targetHuffmanBytes int) string {
	n := int(math.Ceil(float64(targetHuffmanBytes) / avgHuffmanBytesPerCharBase62))
	if n < 1 {
		n = 1
	}
	value, ok := randStringFromCharset(n, charsetBase62)
	if !ok {
		return ""
	}
	const maxIter = 150
	adjustChar := byte('X')
	for iter := 0; iter < maxIter; iter++ {
		diff := int(hpack.HuffmanEncodeLength(value)) - targetHuffmanBytes
		if absInt(diff) <= validationTolerance {
			return value
		}
		if diff < 0 {
			value += string(adjustChar)
			if adjustChar == 'X' {
				adjustChar = 'Z'
			} else {
				adjustChar = 'X'
			}
		} else {
			if len(value) <= 1 {
				return value
			}
			value = value[:len(value)-1]
		}
	}
	return value
}

// 'X' and 'Z' are assigned an 8 bit code in HPACK/QPACK's static huffman table,
// so header compression doesn't change the padding length on the wire.
func generatePadding(method string, length int) string {
	if length <= 0 {
		return ""
	}
	switch method {
	case paddingToken:
		value := generateTokenishPaddingBase62(length)
		if value == "" {
			return strings.Repeat("X", length)
		}
		return value
	default:
		return strings.Repeat("X", length)
	}
}

func (c *config) applyXPaddingToHeader(header http.Header, padding paddingConfig) {
	value := generatePadding(padding.method, padding.length)
	switch padding.placement.placement {
	case placementHeader:
		header.Set(padding.placement.header, value)
	case placementQueryInHeader:
		u, err := url.Parse(padding.placement.rawURL)
		if err != nil || u == nil {
			return
		}
		u.RawQuery = padding.placement.key + "=" + value
		header.Set(padding.placement.header, u.String())
	}
}

func (c *config) clientPadding(rawURL string) paddingConfig {
	padding := paddingConfig{length: int(c.xPaddingRange().rand())}
	if c.xPaddingObfsMode {
		padding.placement = paddingPlacement{
			placement: c.xPaddingPlacement,
			key:       c.xPaddingKey,
			header:    c.xPaddingHeader,
			rawURL:    rawURL,
		}
		padding.method = c.xPaddingMethod
	} else {
		padding.placement = paddingPlacement{
			placement: placementQueryInHeader,
			key:       "x_padding",
			header:    "Referer",
			rawURL:    rawURL,
		}
	}
	return padding
}

func (c *config) applyXPadding(request *http.Request) {
	padding := c.clientPadding(request.URL.String())
	placement := padding.placement.placement
	if placement == placementHeader || placement == placementQueryInHeader {
		c.applyXPaddingToHeader(request.Header, padding)
		return
	}
	value := generatePadding(padding.method, padding.length)
	if value == "" {
		return
	}
	switch placement {
	case placementCookie:
		request.AddCookie(&http.Cookie{Name: padding.placement.key, Value: value, Path: "/"})
	case placementQuery:
		query := request.URL.Query()
		query.Set(padding.placement.key, value)
		request.URL.RawQuery = query.Encode()
	}
}

func (c *config) applyXPaddingToResponse(writer http.ResponseWriter) {
	padding := paddingConfig{length: int(c.xPaddingRange().rand())}
	if c.xPaddingObfsMode {
		padding.placement = paddingPlacement{
			placement: c.xPaddingPlacement,
			key:       c.xPaddingKey,
			header:    c.xPaddingHeader,
		}
		padding.method = c.xPaddingMethod
	} else {
		padding.placement = paddingPlacement{
			placement: placementHeader,
			header:    "X-Padding",
		}
	}
	placement := padding.placement.placement
	if placement == placementHeader || placement == placementQueryInHeader {
		c.applyXPaddingToHeader(writer.Header(), padding)
		return
	}
	value := generatePadding(padding.method, padding.length)
	if value == "" {
		return
	}
	if placement == placementCookie {
		http.SetCookie(writer, &http.Cookie{Name: padding.placement.key, Value: value, Path: "/"})
	}
}

func (c *config) extractXPaddingFromRequest(request *http.Request) (string, string) {
	if !c.xPaddingObfsMode {
		referrer := request.Header.Get("Referer")
		if referrer != "" {
			if referrerURL, err := url.Parse(referrer); err == nil {
				return referrerURL.Query().Get("x_padding"), placementQueryInHeader + "=Referer, key=x_padding"
			}
		} else {
			return request.URL.Query().Get("x_padding"), placementQuery + ", key=x_padding"
		}
	}

	key := c.xPaddingKey
	header := c.xPaddingHeader

	if cookie, err := request.Cookie(key); err == nil && cookie != nil && cookie.Value != "" {
		return cookie.Value, placementCookie + ", key=" + key
	}
	if headerValue := request.Header.Get(header); headerValue != "" {
		if c.xPaddingPlacement == placementHeader {
			return headerValue, placementHeader + "=" + header
		}
		if parsedURL, err := url.Parse(headerValue); err == nil {
			return parsedURL.Query().Get(key), placementQueryInHeader + "=" + header + ", key=" + key
		}
	}
	if queryValue := request.URL.Query().Get(key); queryValue != "" {
		return queryValue, placementQuery + ", key=" + key
	}
	return "", ""
}

func (c *config) isPaddingValid(value string) bool {
	if value == "" {
		return false
	}
	valid := c.xPaddingRange()
	switch c.xPaddingMethod {
	case paddingToken:
		n := int32(hpack.HuffmanEncodeLength(value))
		return n >= max(valid.From-validationTolerance, 0) && n <= valid.To+validationTolerance
	default:
		n := int32(len(value))
		return n >= valid.From && n <= valid.To
	}
}
