package main

import (
	"fmt"
	"os"

	"go.uber.org/fx"

	"wagering-service/internal/app"
	"wagering-service/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fx.New(app.Options(cfg)).Run()
}
