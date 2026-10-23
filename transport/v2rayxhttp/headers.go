package v2rayxhttp

import (
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Browser masquerade for the default request headers, following xray-core's
// common/utils/browser.go ("fetch" variant). A configured User-Agent of
// chrome/firefox/safari/edge/curl/golang selects that profile, any other value is kept.

var (
	chromeVersion = computeChromeVersion()
	chromeUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + strconv.Itoa(chromeVersion) + ".0.0.0 Safari/537.36"
	edgeUA        = chromeUA + "Edg/" + strconv.Itoa(chromeVersion) + ".0.0.0"
	chromeUACH    = greasedUACH(chromeVersion, "chrome")
	edgeUACH      = greasedUACH(chromeVersion, "edge")
	firefoxMajor  = computeFirefoxVersion()
	firefoxUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:" + strconv.Itoa(firefoxMajor) + ".0) Gecko/20100101 Firefox/" + strconv.Itoa(firefoxMajor) + ".0"
	safariUA      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Safari/605.1.15"
	curlUA        = "curl/8.15.0"
)

func computeChromeVersion() int {
	// Chrome 144 was released on 2026-01-13, a new major lands roughly every 35 days.
	start := time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC).Unix() / 86400
	current := time.Now().Unix() / 86400
	diff := int(current-start-35) - int(math.Floor(math.Pow(rand.Float64(), 2)*105))
	return 144 + diff/35
}

func computeFirefoxVersion() int {
	start := time.Date(2024, 7, 29, 0, 0, 0, 0, time.UTC).Unix() / 86400
	current := time.Now().Unix() / 86400
	diff := current - start - 25 - int64(math.Floor(math.Pow(rand.Float64(), 2)*50))
	return int(diff/30) + 128
}

var (
	clientHintGreaseNA  = []string{" ", "(", ":", "-", ".", "/", ")", ";", "=", "?", "_"}
	clientHintVersionNA = []string{"8", "99", "24"}
	clientHintShuffle3  = [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
)

func greasedUACH(major int, fork string) string {
	seed := major
	brands := []string{
		"\"Not" + clientHintGreaseNA[seed%len(clientHintGreaseNA)] + "A" + clientHintGreaseNA[(seed+1)%len(clientHintGreaseNA)] + "Brand\";v=\"" + clientHintVersionNA[seed%len(clientHintVersionNA)] + "\"",
		"\"Chromium\";v=\"" + strconv.Itoa(major) + "\"",
	}
	switch fork {
	case "chrome":
		brands = append(brands, "\"Google Chrome\";v=\""+strconv.Itoa(major)+"\"")
	case "edge":
		brands = append(brands, "\"Microsoft Edge\";v=\""+strconv.Itoa(major)+"\"")
	}
	order := clientHintShuffle3[seed%len(clientHintShuffle3)]
	shuffled := make([]string, len(brands))
	for i, e := range order {
		shuffled[e] = brands[i]
	}
	return strings.Join(shuffled, ", ")
}

func applyDefaultHeaders(header http.Header) {
	browser := ""
	if values := header.Values("User-Agent"); len(values) < 1 {
		browser = "chrome"
	} else {
		switch values[0] {
		case "chrome", "firefox", "safari", "edge", "curl", "golang":
			browser = values[0]
		}
	}
	if browser == "" {
		return // custom User-Agent, keep the headers as configured
	}
	switch browser {
	case "chrome":
		header["Sec-CH-UA"] = []string{chromeUACH}
		header["Sec-CH-UA-Mobile"] = []string{"?0"}
		header["Sec-CH-UA-Platform"] = []string{"\"Windows\""}
		header["DNT"] = []string{"1"}
		header.Set("User-Agent", chromeUA)
		header.Set("Accept-Language", "en-US,en;q=0.9")
	case "edge":
		header["Sec-CH-UA"] = []string{edgeUACH}
		header["Sec-CH-UA-Mobile"] = []string{"?0"}
		header["Sec-CH-UA-Platform"] = []string{"\"Windows\""}
		header["DNT"] = []string{"1"}
		header.Set("User-Agent", edgeUA)
		header.Set("Accept-Language", "en-US,en;q=0.9")
	case "firefox":
		header.Set("User-Agent", firefoxUA)
		header["DNT"] = []string{"1"}
		header.Set("Accept-Language", "en-US,en;q=0.5")
	case "safari":
		header.Set("User-Agent", safariUA)
		header.Set("Accept-Language", "en-US,en;q=0.9")
	case "golang":
		header.Del("User-Agent")
		return
	case "curl":
		header.Set("User-Agent", curlUA)
		return
	}
	applyFetchHeaders(header, browser)
}

func applyFetchHeaders(header http.Header, browser string) {
	header.Set("Sec-Fetch-Mode", "cors")
	header.Set("Sec-Fetch-Dest", "empty")
	header.Set("Sec-Fetch-Site", "same-origin")
	if header.Get("Priority") == "" {
		switch browser {
		case "chrome", "edge":
			header.Set("Priority", "u=1, i")
		case "firefox":
			header.Set("Priority", "u=4")
		case "safari":
			header.Set("Priority", "u=3, i")
		}
	}
	if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", "no-cache")
	}
	if header.Get("Pragma") == "" {
		header.Set("Pragma", "no-cache")
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", "*/*")
	}
}
