package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func MaterializeInputs(ctx context.Context, cfg Config) error {
	for _, input := range cfg.Inputs {
		switch strings.TrimSpace(input.MaterializationMode) {
		case "", "none":
			continue
		case "remote_fetch":
			if err := materializeRemoteFetchInput(ctx, cfg, input); err != nil {
				return err
			}
		case "local_reuse":
			if err := materializeLocalReuseInput(ctx, cfg, input); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: input %s has unsupported materialization mode %q", errMaterializeFailed, input.Name, input.MaterializationMode)
		}
	}
	return nil
}

func materializeRemoteFetchInput(ctx context.Context, cfg Config, input InputSpec) error {
	if strings.TrimSpace(input.URI) == "" {
		return fmt.Errorf("%w: input %s has empty uri", errMaterializeFailed, input.Name)
	}
	if strings.TrimSpace(input.ExpectedDigest) == "" {
		return fmt.Errorf("%w: input %s has empty expected digest", errMaterializeFailed, input.Name)
	}
	if err := validateRemoteFetchURI(cfg, input.URI); err != nil {
		return fmt.Errorf("%w: input %s uri rejected: %v", errMaterializeFailed, input.Name, err)
	}
	workRoot := effectiveWorkRoot(cfg.WorkRoot)
	targetPath, err := materializedInputPath(workRoot, input)
	if err != nil {
		return fmt.Errorf("%w: input %s target path: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir target dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Join(workRoot, ".jumi-fetch"), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir fetch temp dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpFile, err := os.CreateTemp(filepath.Join(workRoot, ".jumi-fetch"), safeInputName(input.Name)+".*.part")
	if err != nil {
		return fmt.Errorf("%w: create temp file for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpName := tmpFile.Name()
	defer func() { _ = os.Remove(tmpName) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URI, nil)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: create request for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	resp, err := remoteFetchClient(cfg).Do(req)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: fetch input %s: %v", errMaterializeFailed, input.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: fetch input %s returned status %d", errMaterializeFailed, input.Name, resp.StatusCode)
	}
	if maxBytes := effectiveHTTPMaxInputBytes(cfg, input); maxBytes > 0 && resp.ContentLength > maxBytes {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: input %s content-length %d exceeds limit %d", errMaterializeFailed, input.Name, resp.ContentLength, maxBytes)
	}

	hash := sha256.New()
	limit := effectiveHTTPMaxInputBytes(cfg, input)
	var bodyReader io.Reader = resp.Body
	if limit > 0 {
		bodyReader = io.LimitReader(resp.Body, limit+1)
	}
	written, err := io.Copy(io.MultiWriter(tmpFile, hash), bodyReader)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: read input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if limit > 0 && written > limit {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: input %s exceeds size limit %d", errMaterializeFailed, input.Name, limit)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: sync input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("%w: close input %s temp file: %v", errMaterializeFailed, input.Name, err)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actualDigest != strings.TrimSpace(input.ExpectedDigest) {
		return fmt.Errorf("%w: input %s digest mismatch: got %s want %s", errMaterializeFailed, input.Name, actualDigest, input.ExpectedDigest)
	}
	if input.ExpectedSizeBytes > 0 && written != input.ExpectedSizeBytes {
		return fmt.Errorf("%w: input %s size mismatch: got %d want %d", errMaterializeFailed, input.Name, written, input.ExpectedSizeBytes)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("%w: move input %s into place: %v", errMaterializeFailed, input.Name, err)
	}
	return nil
}

func materializeLocalReuseInput(_ context.Context, cfg Config, input InputSpec) error {
	if strings.TrimSpace(input.NodeLocalPath) == "" {
		return fmt.Errorf("%w: input %s has empty node-local path", errMaterializeFailed, input.Name)
	}
	if strings.TrimSpace(input.ExpectedDigest) == "" {
		return fmt.Errorf("%w: input %s has empty expected digest", errMaterializeFailed, input.Name)
	}
	if err := validateNodeLocalSourcePath(cfg, input.NodeLocalPath); err != nil {
		return fmt.Errorf("%w: input %s: %v", errMaterializeFailed, input.Name, err)
	}
	workRoot := effectiveWorkRoot(cfg.WorkRoot)
	targetPath, err := materializedInputPath(workRoot, input)
	if err != nil {
		return fmt.Errorf("%w: input %s target path: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir target dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Join(workRoot, ".jumi-fetch"), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir materialize temp dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpFile, err := os.CreateTemp(filepath.Join(workRoot, ".jumi-fetch"), safeInputName(input.Name)+".*.part")
	if err != nil {
		return fmt.Errorf("%w: create temp file for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpName := tmpFile.Name()
	defer func() { _ = os.Remove(tmpName) }()

	sourceFile, err := os.Open(input.NodeLocalPath)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: open node-local input %s: %v", errMaterializeFailed, input.Name, err)
	}
	defer func() { _ = sourceFile.Close() }()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, hash), sourceFile)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: copy node-local input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: sync input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("%w: close input %s temp file: %v", errMaterializeFailed, input.Name, err)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actualDigest != strings.TrimSpace(input.ExpectedDigest) {
		return fmt.Errorf("%w: input %s digest mismatch: got %s want %s", errMaterializeFailed, input.Name, actualDigest, input.ExpectedDigest)
	}
	if input.ExpectedSizeBytes > 0 && written != input.ExpectedSizeBytes {
		return fmt.Errorf("%w: input %s size mismatch: got %d want %d", errMaterializeFailed, input.Name, written, input.ExpectedSizeBytes)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("%w: move input %s into place: %v", errMaterializeFailed, input.Name, err)
	}
	return nil
}

func validateRemoteFetchURI(cfg Config, rawURI string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return err
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("credential-bearing userinfo is not allowed")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if parsed.RawQuery != "" {
		if looksLikeSignedURLQuery(parsed.RawQuery) {
			return fmt.Errorf("signed URL query string is not allowed; use runtime-only credentialRef flow")
		}
		return fmt.Errorf("query string is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
			return fmt.Errorf("host %q is not allowed", host)
		}
	}
	if len(cfg.HTTPAllowedHosts) != 0 {
		allowed := false
		for _, candidate := range cfg.HTTPAllowedHosts {
			if strings.EqualFold(strings.TrimSpace(candidate), host) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("host %q is not in allowlist", host)
		}
	} else if !cfg.HTTPAllowAny {
		return fmt.Errorf("http source allowlist is required")
	}
	return nil
}

func looksLikeSignedURLQuery(rawQuery string) bool {
	rawQuery = strings.ToLower(rawQuery)
	for _, marker := range []string{
		"x-amz-signature",
		"x-amz-credential",
		"x-goog-signature",
		"x-goog-credential",
		"x-ms-signature",
		"signature=",
		"expires=",
	} {
		if strings.Contains(rawQuery, marker) {
			return true
		}
	}
	return false
}

func remoteFetchClient(cfg Config) *http.Client {
	maxRedirects := cfg.HTTPMaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 3
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.ResponseHeaderTimeout = timeout
	transport.IdleConnTimeout = 30 * time.Second
	// Re-validate the resolved IP at dial time to block DNS rebinding attacks:
	// an attacker-controlled DNS server may return a public IP at validation
	// time and a private IP when the actual connection is made.
	// Hosts listed in HTTPAllowedHosts are explicitly trusted and skip the
	// resolved-IP check (allows test servers on loopback).
	dialer := &net.Dialer{}
	allowedHosts := cfg.HTTPAllowedHosts
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		for _, a := range allowedHosts {
			if strings.EqualFold(strings.TrimSpace(a), host) {
				return dialer.DialContext(ctx, network, addr)
			}
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses resolved for host %q", host)
		}
		for _, a := range ips {
			if a.IP.IsLoopback() || a.IP.IsLinkLocalUnicast() || a.IP.IsLinkLocalMulticast() || a.IP.IsPrivate() {
				return nil, fmt.Errorf("host %q resolved to disallowed address %s", host, a.IP)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
				return fmt.Errorf("redirect scheme downgrade is not allowed")
			}
			return validateRemoteFetchURI(cfg, req.URL.String())
		},
	}
}

func effectiveHTTPMaxInputBytes(cfg Config, input InputSpec) int64 {
	if input.ExpectedSizeBytes > 0 {
		return input.ExpectedSizeBytes
	}
	return cfg.HTTPMaxInputBytes
}
