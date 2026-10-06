// Command demo is the entry point for the go-for-java-developer learning project.
//
// It does minimal work by design: parse flags, delegate to Run().
// In Java terms, think of this as a public static void main that only
// wires a Spring ApplicationContext and calls app.run().
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	module := flag.String("module", "all", "which demo to run: all, concurrency, postgres, idempotency")
	flag.Parse()

	if err := Run(*module); err != nil {
		fmt.Fprintln(os.Stderr, "demo failed:", err)
		os.Exit(1)
	}
}

// Run prints pointers to the runnable per-topic examples.
// Each topic directory is itself runnable with `go run ./<dir>`,
// so this command stays thin on purpose.
func Run(module string) error {
	base := "github.com/andreminin/go-for-java-developer"
	examples := map[string][]string{
		"concurrency": {
			"internal/concurrency/goroutines",
			"internal/concurrency/mutex",
			"internal/concurrency/atomic",
			"internal/concurrency/channels",
			"internal/concurrency/problems",
			"internal/concurrency/patterns",
		},
		"postgres": {
			"internal/postgres/isolation",
			"internal/postgres/select_for_update",
			"internal/postgres/outbox",
			"internal/postgres/inbox",
		},
		"idempotency": {
			"internal/idempotency/keys",
			"internal/idempotency/dedup_table",
			"internal/idempotency/middleware",
		},
	}

	printModule := func(name string, dirs []string) {
		fmt.Printf("=== %s ===\n", name)
		for _, d := range dirs {
			fmt.Printf("  go run ./%s\n", d)
		}
	}

	if module == "all" {
		for name, dirs := range examples {
			printModule(name, dirs)
		}
		fmt.Printf("\nModule path base: %s\n", base)
		fmt.Println("Tip: run everything with race detector: go run -race ./internal/concurrency/goroutines")
		return nil
	}

	dirs, ok := examples[module]
	if !ok {
		return fmt.Errorf("unknown module %q: want all, concurrency, postgres or idempotency", module)
	}
	printModule(module, dirs)
	return nil
}
