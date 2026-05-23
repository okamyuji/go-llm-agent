// Package main 09 設計書の Strategy 切替を検証するフィクスチャ
package main

import (
	"fmt"
	"os"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

func main() {
	s1, ok1 := agent.NewStrategy("react", "", "", 0, 0, 0, 0)
	if !ok1 || s1.Name() != "react" {
		fmt.Fprintln(os.Stderr, "react not loaded")
		os.Exit(1)
	}
	s2, ok2 := agent.NewStrategy("planner_executor", "openai/gpt-4o", "openai/gpt-4o-mini", 0, 0, 0, 0)
	if !ok2 || s2.Name() != "planner_executor" {
		fmt.Fprintln(os.Stderr, "planner_executor not loaded")
		os.Exit(2)
	}
	s3, ok3 := agent.NewStrategy("reflection", "", "", 0, 3, 2, 6)
	if !ok3 || s3.Name() != "reflection" {
		fmt.Fprintln(os.Stderr, "reflection not loaded")
		os.Exit(3)
	}
	s4, ok4 := agent.NewStrategy("unknown", "", "", 0, 0, 0, 0)
	if ok4 {
		fmt.Fprintln(os.Stderr, "unknown must not be ok")
		os.Exit(4)
	}
	if s4.Name() != "react" {
		fmt.Fprintln(os.Stderr, "unknown must fallback to react")
		os.Exit(5)
	}
	fmt.Printf("strategies_loaded=%s,%s,%s fallback=%s\n", s1.Name(), s2.Name(), s3.Name(), s4.Name())
}
