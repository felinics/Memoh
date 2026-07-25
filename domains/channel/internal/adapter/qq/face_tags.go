package qq

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
)

var faceTagPattern = regexp.MustCompile(`<faceType=\d+,faceId="[^"]*",ext="([^"]*)">`)

func decodeFaceTag(raw string) (string, error) {
	matches := faceTagPattern.FindStringSubmatch(raw)
	if len(matches) < 2 {
		return "", errors.New("qq face tag ext is missing")
	}
	decoded, err := base64.StdEncoding.DecodeString(matches[1])
	if err != nil {
		return "", err
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return "", err
	}
	if payload.Text == "" {
		return "", errors.New("qq face tag text is empty")
	}
	return payload.Text, nil
}
