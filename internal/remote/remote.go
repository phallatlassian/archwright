// Package remote validates and normalises the git remote URLs Sites are
// registered by (ADR 0019).
package remote

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// scpLike matches `[user@]host:path`, git's shorthand for ssh.
var scpLike = regexp.MustCompile(`^(?:[^@/:\s]+@)?([^@/:\s]+):(.+)$`)

var defaultPorts = map[string]string{"http": "80", "https": "443", "ssh": "22", "git": "9418"}

// Normalize returns the identity of a remote: lowercase host plus path, with
// scheme, user, default port, trailing slashes and ".git" dropped, so that
// `git@host:o/r.git`, `https://host/o/r` and `https://host/o/r.git` are one
// repository. It refuses local paths, file:// URLs and anything that is not
// an http(s), ssh, git or scp-style remote.
func Normalize(raw string) (string, error) {
	if raw != strings.TrimSpace(raw) || raw == "" {
		return "", errors.New("remote URL is empty or has surrounding whitespace")
	}
	if strings.ContainsAny(raw, " \t\r\n\x00") || strings.HasPrefix(raw, "-") {
		return "", errors.New("remote URL is malformed")
	}
	var host, port, path string
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme := strings.ToLower(raw[:i])
		if scheme == "file" {
			return "", errors.New("a file:// URL is refused; Sites are registered by their remote URL, not a local clone (ADR 0019)")
		}
		if _, ok := defaultPorts[scheme]; !ok {
			return "", errors.New("remote URL must be https, http, ssh, git or user@host:path")
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", errors.New("remote URL is malformed")
		}
		if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword || scheme != "ssh" {
				return "", errCredentials(u.Hostname())
			}
		}
		host, port, path = strings.ToLower(u.Hostname()), u.Port(), u.Path
		if port == defaultPorts[scheme] {
			port = ""
		}
	} else {
		m := scpLike.FindStringSubmatch(raw)
		if m == nil || strings.HasPrefix(raw, ".") || strings.HasPrefix(raw, "~") || strings.HasPrefix(m[2], ":") || strings.Contains(raw, `\`) {
			return "", errors.New("not a remote URL; local paths are refused because Sites are registered by remote URL (ADR 0019)")
		}
		if strings.Contains(strings.SplitN(m[2], "/", 2)[0], "@") {
			return "", errors.New("remote URL may not carry credentials; rely on your configured git credentials (ssh keys, credential helper)")
		}
		host, path = strings.ToLower(m[1]), m[2]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if path == "" || slices.Contains(strings.Split(path, "/"), "..") {
		return "", errors.New("remote URL names no repository")
	}
	if port != "" {
		host += ":" + port
	}
	return host + "/" + path, nil
}

func errCredentials(host string) error {
	return fmt.Errorf("remote URL for %s may not carry credentials; rely on your configured git credentials (ssh keys, credential helper)", host)
}

// Name is the repository name a Site defaults to, lowercased: the last path
// segment of a normalised URL.
func Name(raw string) string {
	key, err := Normalize(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(key[strings.LastIndex(key, "/")+1:])
}
