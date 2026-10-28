package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"novelstudio/internal/isolation"
)

// This CLI is an operator entry point. Tool registration/authorization and
// candidate publication remain the caller's responsibility.
func main() {
	os.Exit(run())
}

func run() int {
	runtimeRoot := flag.String("runtime", "", "verified Linux runtime directory")
	requestPath := flag.String("request", "", "approved request JSON file")
	flag.Parse()
	if *runtimeRoot == "" || *requestPath == "" {
		fmt.Fprintln(os.Stderr, "-runtime and -request are required")
		return 2
	}
	file, err := os.Open(*requestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer file.Close()
	request, err := decodeRequest(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	runner, err := isolation.NewRunner(*runtimeRoot, isolation.ApprovedImageSHA256)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, err := runner.Run(ctx, request)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if result.Reason != "completed" || result.ExitCode != 0 {
		return 1
	}
	return 0
}

// Decode into bounded bytes first: a limiting reader alone would accept an
// oversized file whose valid JSON prefix fits below the limit.
func decodeRequest(source io.Reader) (isolation.Request, error) {
	raw, err := io.ReadAll(io.LimitReader(source, isolation.MaxRequestJSONBytes+1))
	if err != nil {
		return isolation.Request{}, err
	}
	if len(raw) > isolation.MaxRequestJSONBytes {
		return isolation.Request{}, errors.New("request JSON exceeds byte limit")
	}
	var wire struct {
		Language       string           `json:"language"`
		Script         string           `json:"script"`
		Files          []isolation.File `json:"files"`
		Entrypoint     string           `json:"entrypoint"`
		Input          []byte           `json:"input"`
		TimeoutMS      int64            `json:"timeout_ms"`
		MaxOutputBytes int              `json:"max_output_bytes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return isolation.Request{}, fmt.Errorf("invalid request JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return isolation.Request{}, errors.New("request must contain one JSON object")
	}
	if wire.TimeoutMS < 100 || wire.TimeoutMS > 30000 {
		return isolation.Request{}, errors.New("timeout_ms outside policy")
	}
	return isolation.Request{Language: wire.Language, Script: wire.Script, Files: wire.Files, Entrypoint: wire.Entrypoint, Input: wire.Input, Timeout: time.Duration(wire.TimeoutMS) * time.Millisecond, MaxOutputBytes: wire.MaxOutputBytes}, nil
}
