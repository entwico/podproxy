package podproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

const pacFetchTimeout = 5 * time.Second

var pacHostPattern = regexp.MustCompile(`shExpMatch\(host,\s*"\*\.([^"]+)"\)`)

func loadPatterns(match, pacURL string, log *logger) []*regexp.Regexp {
	var patterns []*regexp.Regexp

	if match != "" {
		compiled, err := regexp.Compile(match)
		if err != nil {
			log.errorf("invalid GO_PODPROXY_MATCH regexp %q: %v", match, err)
			os.Exit(1)
		}

		patterns = append(patterns, compiled)
	}

	if pacURL != "" {
		pac, err := fetchPac(pacURL)
		if err != nil {
			log.errorf("failed to load PAC from %s: %v", pacURL, err)
		} else {
			patterns = append(patterns, parsePacPatterns(pac)...)
			log.debugf("loaded %d patterns from PAC", len(patterns))
		}
	}

	return patterns
}

func fetchPac(pacURL string) (string, error) {
	// a dedicated transport keeps the fetch off http.DefaultTransport and away from proxy env vars
	client := &http.Client{Timeout: pacFetchTimeout, Transport: &http.Transport{}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, pacURL, nil)
	if err != nil {
		return "", err
	}

	res, err := client.Do(req) //nolint:gosec // the URL is operator-controlled dev config
	if err != nil {
		return "", err
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", res.Status)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func parsePacPatterns(pac string) []*regexp.Regexp {
	matches := pacHostPattern.FindAllStringSubmatch(pac, -1)
	patterns := make([]*regexp.Regexp, 0, len(matches))

	for _, match := range matches {
		patterns = append(patterns, regexp.MustCompile(`\.`+regexp.QuoteMeta(match[1])+`$`))
	}

	return patterns
}
