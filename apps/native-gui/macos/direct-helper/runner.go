package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type Result struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Message string `json:"message,omitempty"`
	State   *State `json:"state,omitempty"`
	Plan    *Plan  `json:"plan,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		printResult(Result{Command: "", Error: "usage: direct-helper start|stop|status|test [--config path]"})
		os.Exit(2)
	}
	command := os.Args[1]
	configPath, err := configPathArg(os.Args[2:])
	if err != nil {
		printResult(Result{Command: command, Error: err.Error()})
		os.Exit(2)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		printResult(Result{Command: command, Error: err.Error()})
		os.Exit(2)
	}
	var result Result
	switch command {
	case "start":
		result = start(cfg)
	case "stop":
		result = stop(cfg)
	case "status":
		result = status(cfg)
	case "test":
		result = test(cfg)
	default:
		result = Result{Command: command, Error: "unknown command"}
	}
	printResult(result)
	if !result.OK {
		os.Exit(1)
	}
}

func configPathArg(args []string) (string, error) {
	if len(args) != 2 || args[0] != "--config" || args[1] == "" {
		return "", fmt.Errorf("expected exactly --config PATH")
	}
	return args[1], nil
}

func printResult(result Result) {
	data, _ := json.Marshal(result)
	fmt.Println(string(data))
}

func start(cfg Config) Result {
	return platformStart(cfg)
}

func stop(cfg Config) Result {
	return platformStop(cfg)
}

func status(cfg Config) Result {
	return platformStatus(cfg)
}

func test(cfg Config) Result {
	plan := cfg.RoutePlan()
	result := Result{OK: true, Command: "test", Message: "configuration and route plan valid", Plan: &plan}
	if err := writeTestResult(cfg, result); err != nil {
		result.OK = false
		result.Error = err.Error()
	}
	return result
}
