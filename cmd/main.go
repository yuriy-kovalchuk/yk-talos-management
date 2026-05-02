package main

import (
	"fmt"
	"os"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/run"
)

func main() {
	if err := run.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
