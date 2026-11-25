// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// The publisher's pull and object store calls are real child processes in these tests. Each fake is a symbolic
// link named for the tool it replaces, pointing at this test binary. TestMain notices the name it was started
// under and behaves as the tool, so the streaming, the deadlines and the exit handling under test are the code
// that ships, with no shell or interpreter between them. A directory stands in for the bucket.
//
// These fakes say what the publisher asks of its tools and how it treats their answers. They say nothing about
// S3, a registry or the HTTPS front end.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "nvkr":
		os.Exit(fakeNvkr(os.Args[1:]))
	case "aws":
		os.Exit(fakeAWS(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeNvkrConfig is what the fake pull does. Image is the file it writes; Result is the text it prints.
type fakeNvkrConfig struct {
	Log      string `json:"log"`
	Exit     int    `json:"exit"`
	Stderr   string `json:"stderr"`
	FileName string `json:"fileName"`
	Image    []byte `json:"image"`
	Symlink  bool   `json:"symlink"`
	Result   string `json:"result"`
}

// failRule makes a fake aws call fail when its joined arguments contain Match.
type failRule struct {
	Match  string `json:"match"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"`
}

// fakeAWSConfig is what the fake object store does. Store is the directory that holds the buckets.
type fakeAWSConfig struct {
	Log         string     `json:"log"`
	Store       string     `json:"store"`
	Fail        []failRule `json:"fail"`
	SlowSeconds int        `json:"slowSeconds"`
	Corrupt     bool       `json:"corrupt"`
	Truncate    bool       `json:"truncate"`
	Drop        bool       `json:"drop"`
	HeadBody    string     `json:"headBody"`
	EnvLog      string     `json:"envLog"`
}

// loadFakeConfig reads <directory of the link>/<name>.json.
func loadFakeConfig(name string, into any) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), name+".json"))
	if err == nil {
		err = json.Unmarshal(data, into)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake %s: no usable configuration: %v\n", name, err)
		os.Exit(98)
	}
}

func appendText(path, text string) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake tool: cannot log: %v\n", err)
		os.Exit(98)
	}
	_, _ = file.WriteString(text)
	_ = file.Close()
}

func appendCall(path string, args []string) {
	line, _ := json.Marshal(args)
	appendText(path, string(line)+"\n")
}

// flagPairs reads the "--name value" pairs that follow a command's first two words.
func flagPairs(args []string) map[string]string {
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i += 2 {
		flags[args[i]] = args[i+1]
	}
	return flags
}

func fakeNvkr(args []string) int {
	var cfg fakeNvkrConfig
	loadFakeConfig("nvkr", &cfg)
	appendCall(cfg.Log, args)
	if cfg.Exit != 0 {
		fmt.Fprint(os.Stderr, cfg.Stderr)
		return cfg.Exit
	}
	if len(args) < 2 {
		return 98
	}
	out := flagPairs(args[2:])["--out"]
	if err := os.Mkdir(out, 0o777); err != nil {
		fmt.Fprintf(os.Stderr, "fake nvkr: %v\n", err)
		return 98
	}
	target := filepath.Join(out, cfg.FileName)
	var err error
	if cfg.Symlink {
		err = os.Symlink("/etc/hosts", target)
	} else {
		err = os.WriteFile(target, cfg.Image, 0o666)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake nvkr: %v\n", err)
		return 98
	}
	_, _ = os.Stdout.WriteString(cfg.Result)
	return 0
}

func fakeAWS(args []string) int {
	var cfg fakeAWSConfig
	loadFakeConfig("aws", &cfg)
	appendCall(cfg.Log, args)
	if cfg.EnvLog != "" {
		value, set := os.LookupEnv("AWS_PAGER")
		appendText(cfg.EnvLog, fmt.Sprintf("%t:%q\n", set, value))
	}
	line := strings.Join(args, " ")
	for _, rule := range cfg.Fail {
		if strings.Contains(line, rule.Match) {
			fmt.Fprint(os.Stderr, rule.Stderr)
			return rule.Code
		}
	}
	objectPath := func(bucket, key string) string {
		return filepath.Join(append([]string{cfg.Store, bucket}, strings.Split(key, "/")...)...)
	}
	split := func(uri string) (string, string) {
		bucket, key, _ := strings.Cut(strings.TrimPrefix(uri, "s3://"), "/")
		return bucket, key
	}
	switch {
	case len(args) >= 2 && args[0] == "s3api" && args[1] == "head-object":
		if cfg.HeadBody != "" {
			fmt.Print(cfg.HeadBody)
			return 0
		}
		flags := flagPairs(args[2:])
		info, err := os.Stat(objectPath(flags["--bucket"], flags["--key"]))
		if err != nil || !info.Mode().IsRegular() {
			fmt.Fprint(os.Stderr, "An error occurred (404) when calling the HeadObject operation: Not Found")
			return 254
		}
		fmt.Printf("{\"ContentLength\": %d}", info.Size())
		return 0
	case len(args) >= 4 && args[0] == "s3" && args[1] == "cp":
		source, destination := args[2], args[3]
		if strings.HasPrefix(source, "s3://") && destination == "-" {
			bucket, key := split(source)
			data, err := os.ReadFile(objectPath(bucket, key))
			if err != nil {
				fmt.Fprint(os.Stderr, "An error occurred (NoSuchKey) when calling the GetObject operation")
				return 1
			}
			if cfg.SlowSeconds > 0 && !strings.HasSuffix(key, ".sha256") {
				time.Sleep(time.Duration(cfg.SlowSeconds) * time.Second)
			}
			_, _ = os.Stdout.Write(data)
			return 0
		}
		bucket, key := split(destination)
		var data []byte
		var err error
		if source == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(source)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake aws: %v\n", err)
			return 98
		}
		if !strings.HasSuffix(key, ".sha256") && len(data) > 0 {
			switch {
			case cfg.Corrupt:
				data[len(data)-1] ^= 1
			case cfg.Truncate:
				data = data[:len(data)-1]
			case cfg.Drop:
				return 0
			}
		}
		target := objectPath(bucket, key)
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			fmt.Fprintf(os.Stderr, "fake aws: %v\n", err)
			return 98
		}
		if err := os.WriteFile(target, data, 0o666); err != nil {
			fmt.Fprintf(os.Stderr, "fake aws: %v\n", err)
			return 98
		}
		return 0
	}
	fmt.Fprint(os.Stderr, "unexpected aws call: "+line)
	return 99
}
